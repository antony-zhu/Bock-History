package agent

import (
	"net"
	"net/http"
	"strings"

	"block.local/block-agent/internal/maintenance"
	"block.local/block-agent/internal/wifi"
)

type networkInterface struct {
	Name string `json:"name"`
	IPv4 string `json:"ipv4"`
}

type bdmConnectivity struct {
	State         string  `json:"state"`
	LastSuccessAt *string `json:"lastSuccessAt"`
	LastError     *string `json:"lastError"`
}

type connectivityResponse struct {
	Interfaces []networkInterface `json:"interfaces"`
	WiFi       wifi.Status        `json:"wifi"`
	BDM        bdmConnectivity    `json:"bdm"`
}

func (r *Runtime) productionSettings(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		writeJSON(writer, http.StatusOK, r.production.Get())
	case http.MethodPatch:
		var patch maintenance.ProductionPatch
		if !decodeJSON(writer, request, &patch) {
			return
		}
		if patch.TargetProduction == nil && patch.ToolChangePieces == nil && patch.InspectionInterval == nil && patch.PiecesPerBox == nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "at least one production value is required"})
			return
		}
		production, err := r.production.Patch(patch)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid production values"})
			return
		}
		writeJSON(writer, http.StatusOK, production)
	default:
		writer.Header().Set("Allow", "GET, PATCH")
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (r *Runtime) connectivity(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(writer, http.StatusOK, r.currentConnectivity(request))
}

func (r *Runtime) connectWiFi(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		SSID     string `json:"ssid"`
		Password string `json:"password"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if strings.TrimSpace(body.SSID) == "" || body.Password == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "SSID and password are required"})
		return
	}
	if r.wifiBackend == nil || r.wifiInterface == "" {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "Wi-Fi connection is unavailable"})
		return
	}
	if _, err := r.wifiBackend.Apply(request.Context(), r.wifiInterface, body.SSID, body.Password); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "Wi-Fi connection failed"})
		return
	}
	writeJSON(writer, http.StatusOK, r.currentConnectivity(request))
}

func (r *Runtime) currentConnectivity(request *http.Request) connectivityResponse {
	wifiStatus := wifi.Status{State: "not_configured", Interface: r.wifiInterface}
	if r.wifiBackend != nil && r.wifiInterface != "" {
		wifiStatus.State = "unknown"
		if status, err := r.wifiBackend.Status(request.Context(), r.wifiInterface); err == nil {
			wifiStatus = status
		}
	}
	bdmStatus := bdmConnectivity{State: "not_configured"}
	if r.mqtt.Enabled {
		bdmStatus.State = "unknown"
	}
	return connectivityResponse{
		Interfaces: localNetworkInterfaces(),
		WiFi:       wifiStatus,
		BDM:        bdmStatus,
	}
}

func localNetworkInterfaces() []networkInterface {
	interfaces, err := net.Interfaces()
	if err != nil {
		return []networkInterface{}
	}
	result := make([]networkInterface, 0, len(interfaces))
	for _, current := range interfaces {
		if current.Flags&net.FlagLoopback != 0 {
			continue
		}
		ipv4 := ""
		addresses, err := current.Addrs()
		if err == nil {
			for _, address := range addresses {
				ip, _, err := net.ParseCIDR(address.String())
				if err == nil && ip.To4() != nil {
					ipv4 = ip.String()
					break
				}
			}
		}
		result = append(result, networkInterface{Name: current.Name, IPv4: ipv4})
	}
	return result
}
