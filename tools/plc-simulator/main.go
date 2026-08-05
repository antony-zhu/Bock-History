package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type registerAssignments map[uint16]uint16

func (a *registerAssignments) String() string {
	return ""
}

func (a *registerAssignments) Set(text string) error {
	addressText, valueText, ok := strings.Cut(text, "=")
	if !ok {
		return fmt.Errorf("register must use ADDRESS=VALUE, got %q", text)
	}
	address, err := strconv.ParseUint(strings.TrimSpace(addressText), 0, 16)
	if err != nil {
		return fmt.Errorf("parse register address: %w", err)
	}
	value, err := strconv.ParseUint(strings.TrimSpace(valueText), 0, 16)
	if err != nil {
		return fmt.Errorf("parse register value: %w", err)
	}
	if *a == nil {
		*a = make(registerAssignments)
	}
	(*a)[uint16(address)] = uint16(value)
	return nil
}

func main() {
	listenAddress := flag.String("listen", "127.0.0.1:1502", "TCP listen address; use the PC Ethernet IP for a direct device link")
	unitID := flag.Uint("unit-id", 1, "Modbus unit ID (1-247)")
	uiAddress := flag.String("ui-address", "127.0.0.1:15080", "local management page listen address (loopback only)")
	pointsFile := flag.String("points-file", "plc-simulator-points.json", "point definition JSON file")
	openUI := flag.Bool("open-ui", true, "open the local management page in the default browser")
	noOpenUI := flag.Bool("no-open-ui", false, "do not open the local management page in the default browser")
	var initialRegisters registerAssignments
	flag.Var(&initialRegisters, "register", "initial holding register, e.g. --register 504=0x0000 (repeatable)")
	flag.Parse()

	if *unitID == 0 || *unitID > 247 {
		fmt.Fprintln(os.Stderr, "--unit-id must be between 1 and 247")
		os.Exit(2)
	}
	if !isLoopbackAddress(*uiAddress) {
		fmt.Fprintln(os.Stderr, "--ui-address must use a loopback host such as 127.0.0.1:15080")
		os.Exit(2)
	}

	server := NewServer(byte(*unitID), initialRegisters, os.Stdout)
	points, err := NewPointManager(*pointsFile, server)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load point definitions: %v\n", err)
		os.Exit(1)
	}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen %s: %v\n", *listenAddress, err)
		os.Exit(1)
	}
	defer listener.Close()
	uiListener, err := net.Listen("tcp", *uiAddress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen UI %s: %v\n", *uiAddress, err)
		os.Exit(1)
	}
	defer uiListener.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	application := NewApplication(server, points, listener.Addr().String())
	uiServer := &http.Server{Handler: application.Handler()}
	uiDone := make(chan error, 1)
	go func() {
		uiDone <- uiServer.Serve(uiListener)
	}()

	fmt.Fprintf(os.Stderr, "PLC simulator listening on %s (unit %d)\n", listener.Addr(), *unitID)
	uiURL := "http://" + uiListener.Addr().String()
	fmt.Fprintf(os.Stderr, "PLC simulator management page listening on %s\n", uiURL)
	if *openUI && !*noOpenUI {
		if err := openBrowser(uiURL); err != nil {
			fmt.Fprintf(os.Stderr, "open management page: %v\n", err)
		}
	}
	if err := server.Serve(ctx, listener); err != nil {
		shutdownUIServer(uiServer)
		fmt.Fprintf(os.Stderr, "serve PLC simulator: %v\n", err)
		os.Exit(1)
	}
	shutdownUIServer(uiServer)
	if err := <-uiDone; err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "serve management page: %v\n", err)
		os.Exit(1)
	}
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func openBrowser(url string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url).Start()
}

func shutdownUIServer(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}
