package main

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/enbility/eebus-go/api"
	"github.com/enbility/eebus-go/service"
	ucapi "github.com/enbility/eebus-go/usecases/api"
	cemcevc "github.com/enbility/eebus-go/usecases/cem/cevc"
	cemevcc "github.com/enbility/eebus-go/usecases/cem/evcc"
	cemevcem "github.com/enbility/eebus-go/usecases/cem/evcem"
	cemevsecc "github.com/enbility/eebus-go/usecases/cem/evsecc"
	eglpc "github.com/enbility/eebus-go/usecases/eg/lpc"
	eglpp "github.com/enbility/eebus-go/usecases/eg/lpp"
	mampc "github.com/enbility/eebus-go/usecases/ma/mpc"

	shipapi "github.com/enbility/ship-go/api"
	"github.com/enbility/ship-go/cert"
	spineapi "github.com/enbility/spine-go/api"
	"github.com/enbility/spine-go/model"
)

var remoteSki string
var enableDebugLogging = true
var enableTraceLogging = true

func writePEMFiles(certificate tls.Certificate, certPath, keyPath string) error {
	// Certificate PEM
	if len(certificate.Certificate) == 0 {
		return fmt.Errorf("no certificate data available")
	}
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certificate.Certificate[0],
	})
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return fmt.Errorf("writing cert: %w", err)
	}

	// Private key PEM (ECDSA)
	privKey, ok := certificate.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return fmt.Errorf("private key is not ECDSA")
	}
	b, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return fmt.Errorf("marshal EC private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: b})
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return fmt.Errorf("writing key: %w", err)
	}

	return nil
}

type hems struct {
	myService *service.Service

	uceglpc     ucapi.EgLPCInterface
	uccemevcc   ucapi.CemEVCCInterface
	uccemevcem  ucapi.CemEVCEMInterface
	uccemevsecc ucapi.CemEVSECCInterface
	uceglpp     ucapi.EgLPPInterface
	uccemcevc   ucapi.CemCEVCInterface
	ucmampc     ucapi.MaMPCInterface

	// in-memory log buffer for trace/debug/info output
	logMu   sync.Mutex
	logs    []string
	maxLogs int

	// websocket clients
	wsMu    sync.Mutex
	wsConns map[*websocket.Conn]struct{}

	// usecase support tracking
	ucMu         sync.Mutex
	usecaseState map[string]bool

	// latest entities from remote device
	entities []spineapi.EntityRemoteInterface

	// last entities JSON payload (cached)
	lastEntitiesJSON []byte
}

func (h *hems) run() {
	var err error
	var certificate tls.Certificate

	exePath, err := os.Executable()
	if err != nil {
		exePath = "."
	}
	exeDir := filepath.Dir(exePath)
	defaultCertPath := filepath.Join(exeDir, "cert.pem")
	defaultKeyPath := filepath.Join(exeDir, "key.pem")

	if len(os.Args) == 5 {
		remoteSki = os.Args[2]

		certificate, err = tls.LoadX509KeyPair(os.Args[3], os.Args[4])
		if err != nil {
			usage()
			log.Fatal(err)
		}
	} else {
		if len(os.Args) >= 3 {
			remoteSki = os.Args[2]
		}
		if _, errCert := os.Stat(defaultCertPath); errCert == nil {
			if _, errKey := os.Stat(defaultKeyPath); errKey == nil {
				certificate, err = tls.LoadX509KeyPair(defaultCertPath, defaultKeyPath)
				if err != nil {
					log.Fatalf("lade cert/key aus %s,%s: %v", defaultCertPath, defaultKeyPath, err)
				}
			}
		}
		if len(certificate.Certificate) == 0 && certificate.PrivateKey == nil {
			certificate, err = cert.CreateCertificate("Demo", "Demo", "DE", "Demo-Unit-01")
			if err != nil {
				log.Fatal(err)
			}

			if err := writePEMFiles(certificate, defaultCertPath, defaultKeyPath); err != nil {
				log.Fatal(err)
			}

			fmt.Printf("Zertifikat geschrieben nach `%s`\n", defaultCertPath)
			fmt.Printf("Private Key geschrieben nach `%s`\n", defaultKeyPath)
		}
	}

	port, err := strconv.Atoi(os.Args[1])
	if err != nil {
		usage()
		log.Fatal(err)
	}

	configuration, err := api.NewConfiguration(
		"Demo", "Demo", "HEMS", "123456789",
		model.DeviceTypeTypeEnergyManagementSystem,
		[]model.EntityTypeType{model.EntityTypeTypeCEM},
		port, certificate, time.Second*30)
	configuration.SetAlternateIdentifier("Demo-HEMS-123456789")
	if err != nil {
		log.Fatal(err)
	}

	h.myService = service.NewService(configuration, h)
	h.myService.SetLogging(h)

	if err = h.myService.Setup(); err != nil {
		fmt.Println(err)
		return
	}

	// initialize log buffer
	h.maxLogs = 1000
	h.logs = make([]string, 0, 200)

	// initialize usecase state map
	h.usecaseState = make(map[string]bool)

	localEntity := h.myService.LocalDevice().EntityForType(model.EntityTypeTypeCEM)

	// CEVC
	h.uccemcevc = cemcevc.NewCEVC(localEntity, h.HandleEgCevc)
	h.myService.AddUseCase(h.uccemcevc)
	h.setUsecaseSupported("CEVC", false)

	// EVCEM
	h.uccemevcem = cemevcem.NewEVCEM(h.myService, localEntity, h.HandleEgEvcem)
	h.myService.AddUseCase(h.uccemevcem)
	h.setUsecaseSupported("EVCEM", false)

	// EVCS
	// TODO: add evcs once supported
	h.setUsecaseSupported("EVCS", false)

	// EVCC
	h.uccemevcc = cemevcc.NewEVCC(h.myService, localEntity, h.HandleEgEvcc)
	h.myService.AddUseCase(h.uccemevcc)
	h.setUsecaseSupported("EVCC", false)

	// EVSECC
	h.uccemevsecc = cemevsecc.NewEVSECC(localEntity, h.HandleEgEvsecc)
	h.myService.AddUseCase(h.uccemevsecc)
	h.setUsecaseSupported("EVSECC", false)

	// LPC
	h.uceglpc = eglpc.NewLPC(localEntity, h.HandleEgLPC)
	h.uceglpc.UpdateUseCaseAvailability(true)
	h.myService.AddUseCase(h.uceglpc)
	h.setUsecaseSupported("LPC", false)

	// LPP
	h.uceglpp = eglpp.NewLPP(localEntity, h.HandleEgLPP)
	h.myService.AddUseCase(h.uceglpp)
	h.setUsecaseSupported("LPP", false)

	// MPC
	h.ucmampc = mampc.NewMPC(localEntity, h.HandleMaMpc)
	h.myService.AddUseCase(h.ucmampc)
	h.setUsecaseSupported("MPC", false)

	if len(remoteSki) == 0 {
		os.Exit(0)
	}

	h.myService.RegisterRemoteSKI(remoteSki)

	h.myService.Start()

	// start web interface in background
	go h.startWebInterface()
	// defer h.myService.Shutdown()
}

// HandleEgLPP Energy Guard LPP Handler
func (h *hems) HandleEgLPP(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgLPP Event: ", event)
	if event == eglpp.UseCaseSupportUpdate {
		h.setUsecaseSupported("LPP", true)
	}
	h.updateEntitiesFromDevice(device)
}

// HandleEgLPC Energy Guard LPC Handler
func (h *hems) HandleEgLPC(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgLPC Event: ", event)
	if event == eglpc.UseCaseSupportUpdate {
		h.setUsecaseSupported("LPC", true)
	}
	h.updateEntitiesFromDevice(device)
}

// HandleEgEvcc Energy Guard EVCC Handler
func (h *hems) HandleEgEvcc(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgEVCC Event: ", event)
	if event == cemevcc.UseCaseSupportUpdate {
		h.setUsecaseSupported("EVCC", true)
	}
	h.updateEntitiesFromDevice(device)
}

// HandleEgEvcem Energy Guard EVCEM Handler
func (h *hems) HandleEgEvcem(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgEVCEM Event: ", event)
	if event == cemevcem.UseCaseSupportUpdate {
		h.setUsecaseSupported("EVCEM", true)
	}
	h.updateEntitiesFromDevice(device)
}

// HandleEgEvsecc Energy Guard EVSECC Handler
func (h *hems) HandleEgEvsecc(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgEVSECC Event: ", event)
	if event == cemevsecc.UseCaseSupportUpdate {
		h.setUsecaseSupported("EVSECC", true)
	}
	h.updateEntitiesFromDevice(device)
}

// HandleEgCevc Energy Guard CEVC Handler
func (h *hems) HandleEgCevc(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgCEVC Event: ", event)
	if event == cemcevc.UseCaseSupportUpdate {
		h.setUsecaseSupported("CEVC", true)
	}
	h.updateEntitiesFromDevice(device)
}

// HandleMaMpc MaMPC Handler
func (h *hems) HandleMaMpc(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("MaMpc Event: ", event)
	if event == mampc.UseCaseSupportUpdate {
		h.setUsecaseSupported("MPC", true)
	}
	h.updateEntitiesFromDevice(device)
}

// Write Functions

func (h *hems) WriteLPCConsumptionLimit(duration int64, value float64) error {
	// iterate remote entities and write the provided consumption limit
	entities := h.uceglpc.RemoteEntitiesScenarios()

	fmt.Println("Writing LPC Consumption Limit:", duration, value)
	fmt.Println("Found entities:", entities)
	var errs []string
	for _, entity := range entities {
		_, err := h.uceglpc.WriteConsumptionLimit(entity.Entity, ucapi.LoadLimit{
			Duration:     time.Duration(duration),
			IsChangeable: false,
			IsActive:     true,
			Value:        value,
		}, nil)
		if err != nil {
			errStr := fmt.Sprintf("%v: %v", entity, err)
			err = fmt.Errorf(errStr)
			errs = append(errs, errStr)
			fmt.Println("Error writing consumption limit:", err)
		} else {
			fmt.Println("Wrote consumption limit to entity", entity)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (h *hems) WriteLPCFailsafe(minDuration time.Duration, failsafePowerLimit float64) {
	// iterate remote entities and write the failsafe
	entities := h.uceglpc.RemoteEntitiesScenarios()
	fmt.Println("Writing LPC Failsafe:", minDuration, failsafePowerLimit)
	fmt.Println("Found entities:", entities)
	for _, entity := range entities {
		_, err := h.uceglpc.WriteFailsafeDurationMinimum(entity.Entity, minDuration)
		if err != nil {
			fmt.Println("Error writing failsafeDurationMinimum:", err)
		} else {
			fmt.Println("Wrote failsafeDurationMinimum to entity", entity)
		}
		_, err = h.uceglpc.WriteFailsafeConsumptionActivePowerLimit(entity.Entity, failsafePowerLimit)
		if err != nil {
			fmt.Println("Error writing FailsafeConsumptionActivePowerLimit:", err)
		} else {
			fmt.Println("Wrote FailsafeConsumptionActivePowerLimit to entity", entity)
		}
	}
}

// EEBUSServiceHandler

func (h *hems) RemoteSKIConnected(service api.ServiceInterface, ski string) {}

func (h *hems) RemoteSKIDisconnected(service api.ServiceInterface, ski string) {}

func (h *hems) VisibleRemoteServicesUpdated(service api.ServiceInterface, entries []shipapi.RemoteService) {
}

func (h *hems) ServiceShipIDUpdate(ski string, shipdID string) {}

func (h *hems) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	if ski == remoteSki && detail.State() == shipapi.ConnectionStateRemoteDeniedTrust {
		fmt.Println("The remote service denied trust. Exiting.")
		h.myService.CancelPairingWithSKI(ski)
		h.myService.UnregisterRemoteSKI(ski)
		h.myService.Shutdown()
		os.Exit(0)
	}
}

func (h *hems) AllowWaitingForTrust(ski string) bool {
	return ski == remoteSki
}

// UCEvseCommisioningConfigurationCemDelegate

// handle device state updates from the remote EVSE device
func (h *hems) HandleEVSEDeviceState(ski string, failure bool, errorCode string) {
	fmt.Println("EVSE Error State:", failure, errorCode)
}

// main app
func usage() {
	fmt.Println("First Run:")
	fmt.Println("  ./device-tester <serverport>")
	fmt.Println()
	fmt.Println("General Usage:")
	fmt.Println("  ./device-tester <serverport> [<remoteski>] [<crtfile> <keyfile>]")
	fmt.Println()
	fmt.Println("If a a cert and key are available in the exe directory as cert.pem and key.pem, they will be used automatically. Otherwise a new self-signed cert will be created and stored there.")

}

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}

	h := hems{}
	h.run()

	// Clean exit to make sure mdns shutdown is invoked
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	// User exit
}

// Logging interface

func (h *hems) Trace(args ...interface{}) {
	// Always broadcast trace messages to frontend, even if tracing is disabled for stdout
	value := fmt.Sprintln(args...)
	// broadcast (append to logs / send to WS)
	ts := h.currentTimestamp()
	line := fmt.Sprintf("%s TRACE %s", ts, value)
	h.appendLog(strings.TrimRight(line, "\n"))
	// still print to stdout if enabled
	if enableTraceLogging {
		fmt.Printf("%s", line)
	}
}

func (h *hems) Tracef(format string, args ...interface{}) {
	// Always broadcast formatted trace to frontend
	value := fmt.Sprintf(format, args...)
	ts := h.currentTimestamp()
	line := fmt.Sprintf("%s TRACEF %s", ts, value)
	h.appendLog(strings.TrimRight(line, "\n"))
	if enableTraceLogging {
		fmt.Println(line)
	}
}

func (h *hems) Debug(args ...interface{}) {
	// Always broadcast debug messages to frontend
	value := fmt.Sprintln(args...)
	ts := h.currentTimestamp()
	line := fmt.Sprintf("%s DEBUG %s", ts, value)
	h.appendLog(strings.TrimRight(line, "\n"))
	if enableDebugLogging {
		fmt.Printf("%s", line)
	}
}

func (h *hems) Debugf(format string, args ...interface{}) {
	// Always broadcast formatted debug messages to frontend
	value := fmt.Sprintf(format, args...)
	ts := h.currentTimestamp()
	line := fmt.Sprintf("%s DEBUGF %s", ts, value)
	h.appendLog(strings.TrimRight(line, "\n"))
	if enableDebugLogging {
		fmt.Println(line)
	}
}

func (h *hems) Info(args ...interface{}) {
	h.print("INFO ", args...)
}

func (h *hems) Infof(format string, args ...interface{}) {
	h.printFormat("INFOF ", format, args...)
}

func (h *hems) Error(args ...interface{}) {
	h.print("ERROR", args...)
}

func (h *hems) Errorf(format string, args ...interface{}) {
	h.printFormat("ERRORF", format, args...)
}

func (h *hems) currentTimestamp() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

func (h *hems) appendLog(line string) {
	h.logMu.Lock()
	defer h.logMu.Unlock()
	if h.maxLogs <= 0 {
		h.maxLogs = 1000
	}
	// keep logs under maxLogs
	if len(h.logs) >= h.maxLogs {
		// drop oldest
		h.logs = h.logs[1:]
	}
	h.logs = append(h.logs, line)

	// broadcast to websocket clients (non-blocking)
	h.wsMu.Lock()
	defer h.wsMu.Unlock()
	for c := range h.wsConns {
		if err := c.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
			// remove broken client
			c.Close()
			delete(h.wsConns, c)
		}
	}
}

func (h *hems) getLogs() []string {
	h.logMu.Lock()
	defer h.logMu.Unlock()
	copyLogs := make([]string, len(h.logs))
	copy(copyLogs, h.logs)
	return copyLogs
}

func (h *hems) print(msgType string, args ...interface{}) {
	value := fmt.Sprintln(args...)
	ts := h.currentTimestamp()
	line := fmt.Sprintf("%s %s %s", ts, msgType, value)
	fmt.Printf("%s", line)
	// also store in in-memory buffer
	h.appendLog(strings.TrimRight(line, "\n"))
}

func (h *hems) printFormat(msgType, format string, args ...interface{}) {
	value := fmt.Sprintf(format, args...)
	ts := h.currentTimestamp()
	line := fmt.Sprintf("%s %s %s", ts, msgType, value)
	fmt.Println(line)
	h.appendLog(line)
}

// setUsecaseSupported updates the internal map and broadcasts the change to websocket clients
func (h *hems) setUsecaseSupported(name string, supported bool) {
	h.ucMu.Lock()
	defer h.ucMu.Unlock()
	// only broadcast if changed
	if old, ok := h.usecaseState[name]; ok && old == supported {
		return
	}
	h.usecaseState[name] = supported
	// broadcast a json message to websocket clients
	msg := map[string]interface{}{"type": "usecase", "name": name, "supported": supported}
	b, err := json.Marshal(msg)
	if err != nil {
		h.Errorf("marshal usecase update: %v", err)
		return
	}

	h.wsMu.Lock()
	defer h.wsMu.Unlock()
	for c := range h.wsConns {
		if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
			c.Close()
			delete(h.wsConns, c)
		}
	}
}

// updateEntitiesFromDevice updates the internal entities slice
func (h *hems) updateEntitiesFromDevice(device spineapi.DeviceRemoteInterface) {
	// update internal entities slice
	h.entities = device.Entities()

	// build JSON-friendly representation
	type OpInfo struct {
		Op   interface{} `json:"op"`
		Name string      `json:"name"`
	}
	type FeatureInfo struct {
		ID         interface{} `json:"id,omitempty"`
		Name       string      `json:"name,omitempty"`
		Roles      interface{} `json:"roles,omitempty"`
		Operations []OpInfo    `json:"operations,omitempty"`
	}
	type EntityInfo struct {
		Address    string        `json:"address"`
		EntityType string        `json:"entityType"`
		Features   []FeatureInfo `json:"features"`
	}

	var out []EntityInfo
	for _, e := range h.entities {
		var ent EntityInfo
		// Address and entity type
		if e != nil {
			ent.Address = fmt.Sprint(e.Address())
			ent.EntityType = fmt.Sprint(e.EntityType())
			// features - e.Features() seems to be an indexed map/array; iterate keys
			for t := range e.Features() {
				f := e.Features()[t]
				if f == nil {
					continue
				}
				var fi FeatureInfo
				// try to include ID if available, otherwise the String()
				fi.Name = fmt.Sprint(f.String())
				// Role() might return a value
				fi.Roles = fmt.Sprint(f.Role())
				// include operations
				for op := range f.Operations() {
					opVal := f.Operations()[op]
					fi.Operations = append(fi.Operations, OpInfo{Op: op, Name: fmt.Sprint(opVal.String())})
				}
				// append to ent.Features
				ent.Features = append(ent.Features, fi)
			}
		}
		out = append(out, ent)
	}

	// marshal and broadcast
	msg := map[string]interface{}{"type": "entities", "entities": out}
	b, err := json.Marshal(msg)
	if err != nil {
		h.Errorf("marshal entities: %v", err)
		return
	}

	// cache last entities json
	h.lastEntitiesJSON = b

	h.wsMu.Lock()
	defer h.wsMu.Unlock()
	for c := range h.wsConns {
		if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
			c.Close()
			delete(h.wsConns, c)
		}
	}
}

// startWebInterface starts a small HTTP server to trigger writes and show logs
func (h *hems) startWebInterface() {
	webPort := 8080
	if v := os.Getenv("WEB_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			webPort = p
		}
	}

	// initialize wsConns map
	h.wsMu.Lock()
	h.wsConns = make(map[*websocket.Conn]struct{})
	h.wsMu.Unlock()

	// Load template from web/index.html (external file) so it can be edited separately
	exePath := "."
	tplPath := filepath.Join(exePath, "web", "index.html")
	tplBytes, err := os.ReadFile(tplPath)
	if err != nil {
		h.Errorf("failed to read web template %s: %v", tplPath, err)
		return
	}
	tmpl := template.Must(template.New("index").Parse(string(tplBytes)))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, nil); err != nil {
			h.Errorf("template execute: %v", err)
		}
	})

	// websocket endpoint for logs
	var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	http.HandleFunc("/ws/logs", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			h.Errorf("ws upgrade: %v", err)
			return
		}
		// add to map
		h.wsMu.Lock()
		h.wsConns[c] = struct{}{}
		h.wsMu.Unlock()

		// send existing logs as initial snapshot
		logs := h.getLogs()
		for _, line := range logs {
			if err := c.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
				break
			}
		}

		// also send current usecase state snapshot
		h.ucMu.Lock()
		for name, supported := range h.usecaseState {
			msg := map[string]interface{}{"type": "usecase", "name": name, "supported": supported}
			if b, err := json.Marshal(msg); err == nil {
				_ = c.WriteMessage(websocket.TextMessage, b)
			}
		}
		h.ucMu.Unlock()

		// send last entities JSON snapshot if available
		if h.lastEntitiesJSON != nil {
			_ = c.WriteMessage(websocket.TextMessage, h.lastEntitiesJSON)
		}

		// read loop to keep connection alive and detect close
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				// client closed or error
				c.Close()
				h.wsMu.Lock()
				delete(h.wsConns, c)
				h.wsMu.Unlock()
				return
			}
		}
	})

	// API endpoint that supports multiple commands as JSON payload
	http.HandleFunc("/api/write", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("invalid json"))
			return
		}
		cmd, _ := payload["cmd"].(string)
		switch cmd {
		case "lpc":
			// extract duration and value
			var dur int64
			var val float64
			if d, ok := payload["duration"].(float64); ok {
				dur = int64(d)
			}
			if v, ok := payload["value"].(float64); ok {
				val = v
			}
			if err := h.WriteLPCConsumptionLimit(dur, val); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(err.Error()))
				return
			}
			_, _ = w.Write([]byte("ok"))
			return
		case "failsafe":
			// extract minDurationMs and failsafePowerLimit
			var durMs int64
			var limit float64
			if d, ok := payload["minDurationMs"].(float64); ok {
				durMs = int64(d)
			}
			if l, ok := payload["failsafePowerLimit"].(float64); ok {
				limit = l
			}
			minDuration := time.Duration(durMs) * time.Millisecond
			h.WriteLPCFailsafe(minDuration, limit)
			_, _ = w.Write([]byte("ok"))
			return
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("unknown command"))
			return
		}
	})

	// keep legacy endpoint
	http.HandleFunc("/writeLpcLimit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("invalid form"))
			return
		}
		durStr := r.FormValue("duration")
		valStr := r.FormValue("value")
		dur, err := strconv.ParseInt(durStr, 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("invalid duration"))
			return
		}
		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("invalid value"))
			return
		}

		err = h.WriteLPCConsumptionLimit(dur, val)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	http.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		logs := h.getLogs()
		type Resp struct {
			Logs []string `json:"logs"`
		}
		enc := Resp{Logs: logs}
		if err := json.NewEncoder(w).Encode(enc); err != nil {
			h.Errorf("encode logs: %v", err)
		}
	})

	// new endpoint: return usecase support state
	http.HandleFunc("/api/usecases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		h.ucMu.Lock()
		defer h.ucMu.Unlock()
		type UC struct {
			Name      string `json:"name"`
			Supported bool   `json:"supported"`
		}
		var out []UC
		for name, supported := range h.usecaseState {
			out = append(out, UC{Name: name, Supported: supported})
		}
		if err := json.NewEncoder(w).Encode(out); err != nil {
			h.Errorf("encode usecases: %v", err)
		}
	})

	// new endpoint: return last known entities JSON
	http.HandleFunc("/api/entities", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if h.lastEntitiesJSON == nil {
			// return empty array
			_, _ = w.Write([]byte("[]"))
			return
		}
		_, _ = w.Write(h.lastEntitiesJSON)
	})

	// Serve static /web assets if needed
	fsDir := filepath.Join(exePath, "web")
	// limit to files in web dir
	fileServer := http.FileServer(http.Dir(fsDir))
	http.Handle("/web/", http.StripPrefix("/web/", fileServer))

	addr := fmt.Sprintf("localhost:%d", webPort)
	h.Infof("Starting web interface on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		h.Errorf("web interface stopped: %v", err)
	}
}
