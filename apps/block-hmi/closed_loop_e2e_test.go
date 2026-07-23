package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFullSimulatorAgentHMIClosedLoop(t *testing.T) {
	goExecutable, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go executable is not available for cross-module black-box test")
	}
	directory, err := os.MkdirTemp("", "blk-full-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	serviceRoot := filepath.Join(repositoryRoot, "services", "block-agent")
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	simulatorBinary := filepath.Join(directory, "plc-simulator"+suffix)
	agentBinary := filepath.Join(directory, "block-agent"+suffix)
	buildGoCommand(t, goExecutable, serviceRoot, simulatorBinary, "./cmd/plc-simulator")
	buildGoCommand(t, goExecutable, serviceRoot, agentBinary, "./cmd/block-agent")

	ioSocket := filepath.Join(directory, "sim-io", "io.sock")
	controlSocket := filepath.Join(directory, "sim-control", "control.sock")
	agentSocket := filepath.Join(directory, "agent-api", "agent.sock")
	simulatorConfig := filepath.Join(directory, "simulator.json")
	agentConfig := filepath.Join(directory, "agent.json")
	writeJSONFile(t, simulatorConfig, map[string]any{
		"ioSocket": ioSocket, "ioSocketGroup": "test-sim-io",
		"controlSocket": controlSocket, "controlSocketGroup": "test-sim-control",
		"stateFile":    filepath.Join(directory, "simulator-state.json"),
		"samplePeriod": "30ms", "seed": 99, "passRate": 1.0,
		"faultInjectionEnabled": true, "binCapacities": []int{100, 100, 20},
		"initialTarget": 50, "initialCycleSeconds": 1,
		"initialToolLimit": 100, "initialInspectInterval": 5,
	})
	writeJSONFile(t, agentConfig, map[string]any{
		"siteId": "site-e2e", "blockId": "block-e2e", "deviceId": "device-e2e",
		"adapter":        map[string]any{"type": "simulator", "ioSocket": ioSocket},
		"localApiSocket": agentSocket, "localApiSocketGroup": "test-hmi-api",
		"databasePath": filepath.Join(directory, "block.db"),
		"samplePeriod": "30ms", "staleAfter": "150ms", "commandTimeout": "120ms",
	})

	simulator := startChild(t, simulatorBinary, "-config", simulatorConfig)
	defer simulator.Stop()
	waitForPath(t, ioSocket, 3*time.Second, simulator)
	agent := startChild(t, agentBinary, "-config", agentConfig)
	defer agent.Stop()
	waitForPath(t, agentSocket, 3*time.Second, agent)

	controller, err := newAgentController(agentSocket, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	source, err := controller.SourceInfo(context.Background())
	if err != nil || !source.Simulation || source.Kind != "simulator" {
		t.Fatalf("Agent source metadata = %+v, %v", source, err)
	}
	handler, err := newHandlerWithOptions(controller, defaultBasePath, source.Simulation)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	caCertificate, caKey, caPEM := makeCA(t, now)
	serverCertificate := makeServerCertificate(t, caCertificate, caKey, now.Add(-time.Hour), now.Add(time.Hour))
	hmiAddress, stopHMI := startTLSServer(t, handler, serverCertificate)
	defer stopHMI()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	hmiClient := tlsHTTPClient(roots, "", tls.VersionTLS12, 0)
	baseURL := "https://" + hmiAddress

	initial := waitHMIState(t, hmiClient, baseURL, http.StatusOK, 3*time.Second)
	commandBody, _ := json.Marshal(map[string]any{"command": "start", "expectedRevision": initial.Revision})
	request, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/commands", bytes.NewReader(commandBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Operator", "E2E")
	request.Header.Set("Idempotency-Key", "full-loop-start")
	response, err := hmiClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HMI start status = %d: %s", response.StatusCode, responseText(response))
	}
	_ = response.Body.Close()
	produced := waitHMIOutput(t, hmiClient, baseURL, 1, 3*time.Second)

	simulator.Stop()
	waitHMIState(t, hmiClient, baseURL, http.StatusServiceUnavailable, 3*time.Second)
	if produced.Output < 1 {
		t.Fatal("last good HMI snapshot was empty before disconnect")
	}

	simulator = startChild(t, simulatorBinary, "-config", simulatorConfig)
	defer simulator.Stop()
	waitForPath(t, ioSocket, 3*time.Second, simulator)
	waitHMIState(t, hmiClient, baseURL, http.StatusOK, 3*time.Second)

	agent.Stop()
	agent = startChild(t, agentBinary, "-config", agentConfig)
	defer agent.Stop()
	waitForPath(t, agentSocket, 3*time.Second, agent)
	restored := waitHMIState(t, hmiClient, baseURL, http.StatusOK, 3*time.Second)
	if restored.Output < produced.Output {
		t.Fatalf("Agent restart lost SQLite snapshot: before=%d after=%d", produced.Output, restored.Output)
	}
	request, _ = http.NewRequest(http.MethodPost, baseURL+"/api/v1/commands", bytes.NewReader(commandBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Operator", "E2E")
	request.Header.Set("Idempotency-Key", "full-loop-start")
	response, err = hmiClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("deduplicated command after Agent restart status = %d: %s", response.StatusCode, responseText(response))
	}
	_ = response.Body.Close()
}

type childProcess struct {
	command *exec.Cmd
	stderr  bytes.Buffer
	stopped bool
}

func startChild(t *testing.T, executable string, arguments ...string) *childProcess {
	t.Helper()
	child := &childProcess{command: exec.Command(executable, arguments...)}
	child.command.Stderr = &child.stderr
	if err := child.command.Start(); err != nil {
		t.Fatal(err)
	}
	return child
}

func (c *childProcess) Stop() {
	if c == nil || c.stopped {
		return
	}
	c.stopped = true
	if c.command.Process != nil {
		_ = c.command.Process.Kill()
	}
	_ = c.command.Wait()
}

func buildGoCommand(t *testing.T, goExecutable, directory, output, packagePath string) {
	t.Helper()
	command := exec.Command(goExecutable, "build", "-trimpath", "-o", output, packagePath)
	command.Dir = directory
	contents, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", packagePath, err, contents)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForPath(t *testing.T, path string, timeout time.Duration, child *childProcess) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if child.command.ProcessState != nil && child.command.ProcessState.Exited() {
			t.Fatalf("child exited before %s became ready: %s", path, child.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not become ready: %s", path, child.stderr.String())
}

func waitHMIState(t *testing.T, client *http.Client, baseURL string, status int, timeout time.Duration) HMIState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastStatus int
	for time.Now().Before(deadline) {
		response, err := client.Get(baseURL + "/api/v1/state")
		if err == nil {
			lastStatus = response.StatusCode
			if lastStatus == status {
				if status != http.StatusOK {
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					return HMIState{}
				}
				var payload struct {
					State HMIState `json:"state"`
				}
				if json.NewDecoder(response.Body).Decode(&payload) == nil {
					_ = response.Body.Close()
					return payload.State
				}
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("HMI state did not return %d; last status %d", status, lastStatus)
	return HMIState{}
}

func waitHMIOutput(t *testing.T, client *http.Client, baseURL string, minimum int, timeout time.Duration) HMIState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state := waitHMIState(t, client, baseURL, http.StatusOK, time.Second)
		if state.Output >= minimum {
			return state
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("HMI output did not reach %d", minimum)
	return HMIState{}
}

func responseText(response *http.Response) string {
	defer response.Body.Close()
	contents, _ := io.ReadAll(response.Body)
	return fmt.Sprintf("%s", contents)
}
