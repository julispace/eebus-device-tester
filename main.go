package main

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	cemevsoc "github.com/enbility/eebus-go/usecases/cem/evsoc"
	cemopev "github.com/enbility/eebus-go/usecases/cem/opev"
	cemoscev "github.com/enbility/eebus-go/usecases/cem/oscev"
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
	mamgrp "github.com/enbility/eebus-go/usecases/ma/mgcp"
	mampc "github.com/enbility/eebus-go/usecases/ma/mpc"

	shipapi "github.com/enbility/ship-go/api"
	"github.com/enbility/ship-go/cert"
	spineapi "github.com/enbility/spine-go/api"
	"github.com/enbility/spine-go/model"
)

// Config represents the application configuration
type Config struct {
	Usecases map[string]UsecaseConfig `json:"usecases"`
	Logging  LoggingConfig            `json:"logging"`
}

// UsecaseConfig represents configuration for a single usecase
type UsecaseConfig struct {
	Enabled     bool   `json:"enabled"`
	Description string `json:"description"`
}

// LoggingConfig represents logging configuration
type LoggingConfig struct {
	EnableDebug bool `json:"enableDebug"`
	EnableTrace bool `json:"enableTrace"`
}

// loadConfig loads the config.json file or returns default config if file doesn't exist
func loadConfig() (*Config, error) {
	configPath := "config.json"
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Return default config with all usecases enabled
			fmt.Println("config.json not found, using default configuration (all usecases enabled)")
			return getDefaultConfig(), nil
		}
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	fmt.Println("Configuration loaded from config.json")
	return &cfg, nil
}

// getDefaultConfig returns a config with all usecases enabled
func getDefaultConfig() *Config {
	return &Config{
		Usecases: map[string]UsecaseConfig{
			"lpc":    {Enabled: true, Description: "Limitation of Power Consumption (EG)"},
			"lpp":    {Enabled: true, Description: "Limitation of Power Production (EG)"},
			"evcc":   {Enabled: true, Description: "EV Commissioning and Configuration (CEM)"},
			"evcem":  {Enabled: true, Description: "EV Charging Electricity Measurement (CEM)"},
			"evsecc": {Enabled: true, Description: "EVSE Commissioning and Configuration (CEM)"},
			"cevc":   {Enabled: true, Description: "Coordinated EV Charging (CEM)"},
			"opev":   {Enabled: true, Description: "Overload Protection by EV Charging Current Curtailment (CEM)"},
			"oscev":  {Enabled: true, Description: "Optimization of Self-Consumption During EV Charging (CEM)"},
			"evsoc":  {Enabled: true, Description: "EV State Of Charge (CEM)"},
			"mpc":    {Enabled: true, Description: "Monitoring of Power Consumption (MA)"},
			"mgcp":   {Enabled: true, Description: "Monitoring of Grid Connection Point (MA)"},
		},
		Logging: LoggingConfig{
			EnableDebug: true,
			EnableTrace: true,
		},
	}
}

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

type usecaseData struct {
	// LPC usecase data
	LpcFailsafePower              float64       `json:"lpcFailsafePower,omitempty"`
	LpcFailsafeDur                time.Duration `json:"lpcFailsafeDurMinutes,omitempty"`
	LpcLimitValue                 float64       `json:"lpcLimitValue,omitempty"`
	LpcLimitDurSeconds            time.Duration `json:"lpcLimitDurSeconds,omitempty"`
	LpcLimitActive                bool          `json:"lpcLimitActive"`
	LpcConsumptionLimitNominalMax float64       `json:"lpcConsumptionLimitNominalMax,omitempty"`
	LpcHeartbeatOk                bool          `json:"lpcHeartbeatOk"`
	LpcHeartbeatTimestamp         time.Time     `json:"lpcHeartbeatTimestamp,omitempty"`
	// LPP usecase data
	LppFailsafeDur        time.Duration `json:"lppFailsafeDurMinutes,omitempty"`
	LppFailsafeValue      float64       `json:"lppFailsafeValue,omitempty"`
	LppLimitValue         float64       `json:"lppLimitValue,omitempty"`
	LppLimitDuration      time.Duration `json:"lppLimitDurationSeconds,omitempty"`
	LppLimitActive        bool          `json:"lppLimitActive"`
	LppHeartbeatOk        bool          `json:"lppHeartbeatOk"`
	LppHeartbeatTimestamp time.Time     `json:"lppHeartbeatTimestamp,omitempty"`
	// EVSECC usecase data
	EvseccManufacturerData          ucapi.ManufacturerData `json:"evseccManufacturerData,omitempty"`
	EvseccOperatingState            string                 `json:"evseccOperatingState,omitempty"`
	EvseccOperatingStateDescription string                 `json:"evseccOperatingStateDescription,omitempty"`
	// EVCC usecase data
	EvccManufacturerData          ucapi.ManufacturerData     `json:"evccManufacturerData,omitempty"`
	EvccChargeState               string                     `json:"evccChargeState"`
	EvccAsymmetricChargingSupport bool                       `json:"evccAsymmetricChargingSupport,omitempty"`
	EvccCommunicationStandard     string                     `json:"evccCommunicationStandard,omitempty"`
	EvccLimitMinimum              float64                    `json:"evccLimitMinimum,omitempty"`
	EvccLimitMaximum              float64                    `json:"evccLimitMaximum,omitempty"`
	EvccLimitStandby              float64                    `json:"evccLimitStandby,omitempty"`
	EvccIdentifications           []ucapi.IdentificationItem `json:"evccIdentifications,omitempty"`
	EvccSleepMode                 bool                       `json:"evccSleepMode"`
	EvccEvConnected               bool                       `json:"evccEvConnected"`
	// EVCEM usecase data
	EvcemCurrentPerPhase []float64 `json:"evcemCurrentPerPhase,omitempty"`
	EvcemPhasesConnected uint      `json:"evcemPhasesConnected,omitempty"`
	EvcemEnergyCharged   float64   `json:"evcemEnergyCharged,omitempty"`
	EvcemPowerPerPhase   []float64 `json:"evcemPowerPerPhase,omitempty"`
	// MPC usecase data
	MpcPowerPerPhase   []float64 `json:"mpcPowerPerPhase,omitempty"`
	MpcCurrentPerPhase []float64 `json:"mpcCurrentPerPhase,omitempty"`
	MpcPower           float64   `json:"mpcPower,omitempty"`
	MpcFrequency       float64   `json:"mpcFrequency,omitempty"`
	MpcVoltagePerPhase []float64 `json:"mpcVoltagePerPhase,omitempty"`
	MpcEnergyConsumed  float64   `json:"mpcEnergyConsumed,omitempty"`
	MpcEnergyProduced  float64   `json:"mpcEnergyProduced,omitempty"`
	// MGCP usecase data
	MgcPowerLimitationFactor float64   `json:"mgcPowerLimitationFactor,omitempty"`
	MgcPower                 float64   `json:"mgcPower,omitempty"`
	MgcEnergyFeedIn          float64   `json:"mgcEnergyFeedIn,omitempty"`
	MgcEnergyConsumed        float64   `json:"mgcEnergyConsumed,omitempty"`
	MgcCurrentPerPhase       []float64 `json:"mgcCurrentPerPhase,omitempty"`
	MgcVoltagePerPhase       []float64 `json:"mgcVoltagePerPhase,omitempty"`
	MgcFrequency             float64   `json:"mgcFrequency,omitempty"`
	// OPEV usecase data
	OpevLoadControlLimit    []ucapi.LoadLimitsPhase `json:"opevLoadControlLimit,omitempty"`
	OpevCurrentLimitMin     []float64               `json:"opevCurrentLimitMin,omitempty"`
	OpevCurrentLimitMax     []float64               `json:"opevCurrentLimitMax,omitempty"`
	OpevCurrentLimitDefault []float64               `json:"opevCurrentLimitDefault,omitempty"`
	// OSCEV usecase data
	OscevLoadControlLimit    []ucapi.LoadLimitsPhase `json:"oscevLoadControlLimit,omitempty"`
	OscevCurrentLimitMin     []float64               `json:"oscevCurrentLimitMin,omitempty"`
	OscevCurrentLimitMax     []float64               `json:"oscevCurrentLimitMax,omitempty"`
	OscevCurrentLimitDefault []float64               `json:"oscevCurrentLimitDefault,omitempty"`
	// EVSOC usecase data
	EvsocStateOfCharge float64 `json:"evsocStateOfCharge,omitempty"`
	// CEVC usecase data
	CevcChargeStrategy        string                         `json:"cevcChargeStrategy,omitempty"`
	CevcEnergyDemand          ucapi.Demand                   `json:"cevcEnergyDemand,omitempty"`
	CevcTimeSlotConstraints   ucapi.TimeSlotConstraints      `json:"cevcTimeSlotConstraints,omitempty"`
	CevcIncentiveConstraints  ucapi.IncentiveSlotConstraints `json:"cevcIncentiveConstraints,omitempty"`
	CevcChargePlanConstraints []ucapi.DurationSlotValue      `json:"cevcChargePlanConstraints,omitempty"`
	CevcChargePlan            ucapi.ChargePlan               `json:"cevcChargePlan,omitempty"`
}

// peerData holds all data for a single peer connection
type peerData struct {
	usecaseData      usecaseData
	entities         []spineapi.EntityRemoteInterface
	lastEntitiesJSON []byte
	usecaseState     map[string]bool
	connected        bool
	ski              string
	lastSeen         time.Time
	deviceName       string
	brand            string
	model            string
	deviceType       string
	serial           string
	identifier       string
}

// PeerInfo represents peer information for API responses
type PeerInfo struct {
	SKI        string          `json:"ski"`
	Connected  bool            `json:"connected"`
	LastSeen   time.Time       `json:"lastSeen"`
	Usecases   map[string]bool `json:"usecases"`
	DeviceName string          `json:"deviceName"`
	Brand      string          `json:"brand"`
	Model      string          `json:"model"`
	DeviceType string          `json:"deviceType"`
	Serial     string          `json:"serial"`
	Identifier string          `json:"identifier"`
}

type hems struct {
	myService *service.Service

	uceglpc     ucapi.EgLPCInterface
	uccemevcc   ucapi.CemEVCCInterface
	uccemevcem  ucapi.CemEVCEMInterface
	uccemevsecc ucapi.CemEVSECCInterface
	uccemopev   ucapi.CemOPEVInterface
	uccemoscev  ucapi.CemOSCEVInterface
	uccemevsoc  ucapi.CemEVSOCInterface
	uceglpp     ucapi.EgLPPInterface
	uccemcevc   ucapi.CemCEVCInterface
	ucmampc     ucapi.MaMPCInterface
	ucmamgrp    ucapi.MaMGCPInterface

	// in-memory log buffer for trace/debug/info output
	logMu   sync.Mutex
	logs    []string
	maxLogs int

	// websocket clients
	wsMu    sync.Mutex
	wsConns map[*websocket.Conn]struct{}

	// peers management
	peers              map[string]*peerData
	peersMu            sync.Mutex
	globalUseCaseState map[string]bool

	// configuration
	config *Config
}

// getOrCreatePeer gets an existing peer or creates a new one
func (h *hems) getOrCreatePeer(ski string) *peerData {
	h.peersMu.Lock()
	defer h.peersMu.Unlock()

	if h.peers == nil {
		h.peers = make(map[string]*peerData)
	}

	if peer, exists := h.peers[ski]; exists {
		peer.lastSeen = time.Now()
		return peer
	}

	peer := &peerData{
		usecaseData:  usecaseData{},
		entities:     nil,
		usecaseState: make(map[string]bool),
		connected:    false,
		ski:          ski,
		lastSeen:     time.Now(),
	}
	h.peers[ski] = peer

	// Initialize usecase states from global state
	ucMu.Lock()
	for uc, supported := range h.globalUseCaseState {
		peer.usecaseState[uc] = supported
	}
	ucMu.Unlock()

	return peer
}

// getPeer gets an existing peer or nil if not found
func (h *hems) getPeer(ski string) *peerData {
	h.peersMu.Lock()
	defer h.peersMu.Unlock()

	if h.peers == nil {
		return nil
	}

	if peer, exists := h.peers[ski]; exists {
		return peer
	}
	return nil
}

// removePeer removes a peer from the map
func (h *hems) removePeer(ski string) {
	h.peersMu.Lock()
	defer h.peersMu.Unlock()

	if h.peers != nil {
		delete(h.peers, ski)
	}
}

// getAllPeers returns a copy of all peers
func (h *hems) getAllPeers() map[string]*peerData {
	h.peersMu.Lock()
	defer h.peersMu.Unlock()

	if h.peers == nil {
		return make(map[string]*peerData)
	}

	peersCopy := make(map[string]*peerData, len(h.peers))
	for k, v := range h.peers {
		peersCopy[k] = v
	}
	return peersCopy
}

// usecase update mutex (reused for global state)
var ucMu sync.Mutex

// usecaseDataMutex for thread-safe access to peer usecase data
var usecaseDataMutex sync.Mutex

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

	if len(os.Args) == 4 {
		certificate, err = tls.LoadX509KeyPair(os.Args[2], os.Args[3])
		if err != nil {
			usage()
			log.Fatal(err)
		}
	} else {
		if _, errCert := os.Stat(defaultCertPath); errCert == nil {
			if _, errKey := os.Stat(defaultKeyPath); errKey == nil {
				certificate, err = tls.LoadX509KeyPair(defaultCertPath, defaultKeyPath)
				if err != nil {
					log.Fatalf("loading cert/key from %s,%s: %v", defaultCertPath, defaultKeyPath, err)
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

			fmt.Printf("Certificate written to `%s`\n", defaultCertPath)
			fmt.Printf("Private Key written to `%s`\n", defaultKeyPath)
		}
	}

	port, err := strconv.Atoi(os.Args[1])
	if err != nil {
		usage()
		log.Fatal(err)
	}

	configuration, err := api.NewConfiguration(
		"DemoVendor", "DemoBrand", "Device-Tester", "123456789",
		[]shipapi.DeviceCategoryType{shipapi.DeviceCategoryTypeEMobility},
		model.DeviceTypeTypeEnergyManagementSystem,
		[]model.EntityTypeType{model.EntityTypeTypeCEM},
		port, certificate, time.Second*30)
	if err != nil {
		log.Fatal(err)
	}
	configuration.SetAlternateIdentifier("Demo-HEMS-123456789")

	h.myService = service.NewService(configuration, h)
	h.myService.SetLogging(h)

	if err = h.myService.Setup(); err != nil {
		fmt.Println(err)
		return
	}

	// initialize log buffer
	h.maxLogs = 1000
	h.logs = make([]string, 0, 200)

	// initialize global usecase state map
	h.globalUseCaseState = make(map[string]bool)

	// load configuration
	h.config, err = loadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		log.Fatal(err)
	}

	localEntity := h.myService.LocalDevice().EntityForType(model.EntityTypeTypeCEM)

	// Helper function to check if usecase is enabled
	isEnabled := func(name string) bool {
		if cfg, ok := h.config.Usecases[name]; ok {
			return cfg.Enabled
		}
		return true // default to enabled if not in config
	}

	// CEVC
	if isEnabled("cevc") {
		h.uccemcevc = cemcevc.NewCEVC(localEntity, h.HandleEgCevc)
		h.myService.AddUseCase(h.uccemcevc)
		h.setUsecaseSupported("CEVC", false)
		fmt.Println("Usecase CEVC enabled")
	} else {
		fmt.Println("Usecase CEVC disabled by config")
	}

	// EVCEM
	if isEnabled("evcem") {
		h.uccemevcem = cemevcem.NewEVCEM(h.myService, localEntity, h.HandleEgEvcem)
		h.myService.AddUseCase(h.uccemevcem)
		h.setUsecaseSupported("EVCEM", false)
		fmt.Println("Usecase EVCEM enabled")
	} else {
		fmt.Println("Usecase EVCEM disabled by config")
	}

	// EVCS
	// TODO: add evcs once supported
	h.setUsecaseSupported("EVCS", false)

	// EVCC
	if isEnabled("evcc") {
		h.uccemevcc = cemevcc.NewEVCC(h.myService, localEntity, h.HandleEgEvcc)
		h.myService.AddUseCase(h.uccemevcc)
		h.setUsecaseSupported("EVCC", false)
		fmt.Println("Usecase EVCC enabled")
	} else {
		fmt.Println("Usecase EVCC disabled by config")
	}

	// EVSECC
	if isEnabled("evsecc") {
		h.uccemevsecc = cemevsecc.NewEVSECC(localEntity, h.HandleEgEvsecc)
		h.myService.AddUseCase(h.uccemevsecc)
		h.setUsecaseSupported("EVSECC", false)
		fmt.Println("Usecase EVSECC enabled")
	} else {
		fmt.Println("Usecase EVSECC disabled by config")
	}

	// LPC
	if isEnabled("lpc") {
		h.uceglpc = eglpc.NewLPC(localEntity, h.HandleEgLPC)
		h.myService.AddUseCase(h.uceglpc)
		h.setUsecaseSupported("LPC", false)
		fmt.Println("Usecase LPC enabled")
	} else {
		fmt.Println("Usecase LPC disabled by config")
	}

	// LPP
	if isEnabled("lpp") {
		h.uceglpp = eglpp.NewLPP(localEntity, h.HandleEgLPP)
		h.myService.AddUseCase(h.uceglpp)
		h.setUsecaseSupported("LPP", false)
		fmt.Println("Usecase LPP enabled")
	} else {
		fmt.Println("Usecase LPP disabled by config")
	}

	// MPC
	if isEnabled("mpc") {
		h.ucmampc = mampc.NewMPC(localEntity, h.HandleMaMpc)
		h.myService.AddUseCase(h.ucmampc)
		h.setUsecaseSupported("MPC", false)
		fmt.Println("Usecase MPC enabled")
	} else {
		fmt.Println("Usecase MPC disabled by config")
	}

	// MGCP
	if isEnabled("mgcp") {
		h.ucmamgrp = mamgrp.NewMGCP(localEntity, h.HandleMaMGCP)
		h.myService.AddUseCase(h.ucmamgrp)
		h.setUsecaseSupported("MGCP", false)
		fmt.Println("Usecase MGCP enabled")
	} else {
		fmt.Println("Usecase MGCP disabled by config")
	}

	// OPEV
	if isEnabled("opev") {
		h.uccemopev = cemopev.NewOPEV(localEntity, h.HandleCemOpev)
		h.myService.AddUseCase(h.uccemopev)
		h.setUsecaseSupported("OPEV", false)
		fmt.Println("Usecase OPEV enabled")
	} else {
		fmt.Println("Usecase OPEV disabled by config")
	}

	// OSCEV
	if isEnabled("oscev") {
		h.uccemoscev = cemoscev.NewOSCEV(localEntity, h.HandleCemOscev)
		h.myService.AddUseCase(h.uccemoscev)
		h.setUsecaseSupported("OSCEV", false)
		fmt.Println("Usecase OSCEV enabled")
	} else {
		fmt.Println("Usecase OSCEV disabled by config")
	}

	// EVSOC
	if isEnabled("evsoc") {
		h.uccemevsoc = cemevsoc.NewEVSOC(localEntity, h.HandleCemEvsoc)
		h.myService.AddUseCase(h.uccemevsoc)
		h.setUsecaseSupported("EVSOC", false)
		fmt.Println("Usecase EVSOC enabled")
	} else {
		fmt.Println("Usecase EVSOC disabled by config")
	}

	h.myService.Start()

	// start web interface in background
	go h.startWebInterface()
	// defer h.myService.Shutdown()
}

// HandleEgLPP Energy Guard LPP Handler
func (h *hems) HandleEgLPP(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgLPP Event: ", event)

	peer := h.getOrCreatePeer(ski)

	if event == eglpp.UseCaseSupportUpdate {
		h.setUsecaseSupportedForPeer(peer, "LPP", true)
	}
	switch event {
	case eglpp.UseCaseSupportUpdate:
		h.setUsecaseSupportedForPeer(peer, "LPP", true)
	case eglpp.DataUpdateFailsafeDurationMinimum:
		minDur, err := h.uceglpp.FailsafeDurationMinimum(entity)
		if err != nil {
			fmt.Println("Error getting FailsafeDurationMinimum:", err)
		} else {
			peer.usecaseData.LppFailsafeDur = minDur / time.Minute
		}
	case eglpp.DataUpdateFailsafeProductionActivePowerLimit:
		powerLimit, err := h.uceglpp.FailsafeProductionActivePowerLimit(entity)
		if err != nil {
			fmt.Println("Error getting FailsafeConsumptionActivePowerLimit:", err)
		} else {
			peer.usecaseData.LppFailsafeValue = powerLimit
		}
	case eglpp.DataUpdateLimit:
		limit, err := h.uceglpp.ProductionLimit(entity)
		if err != nil {
			fmt.Println("Error getting ProductionNominalMax:", err)
		} else {
			peer.usecaseData.LppLimitValue = limit.Value
			peer.usecaseData.LppLimitDuration = limit.Duration / time.Second
			peer.usecaseData.LppLimitActive = limit.IsActive
		}
	case eglpp.DataUpdateHeartbeat:
		peer.usecaseData.LppHeartbeatOk = h.uceglpp.IsHeartbeatWithinDuration(entity)
		peer.usecaseData.LppHeartbeatTimestamp = time.Now()
	}
	h.updateEntitiesFromDevice(ski, device, peer)
}

// HandleEgLPC Energy Guard LPC Handler
func (h *hems) HandleEgLPC(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgLPC Event: ", event)

	peer := h.getOrCreatePeer(ski)

	switch event {
	case eglpc.UseCaseSupportUpdate:
		h.setUsecaseSupportedForPeer(peer, "LPC", true)
	case eglpc.DataUpdateLimit:
		limit, err := h.uceglpc.ConsumptionLimit(entity)
		if err != nil {
			fmt.Println("Error getting ConsumptionNominalMax:", err)
		} else {
			peer.usecaseData.LpcLimitActive = limit.IsActive
			peer.usecaseData.LpcLimitDurSeconds = limit.Duration / time.Second
			peer.usecaseData.LpcLimitValue = limit.Value
		}
	case eglpc.DataUpdateFailsafeDurationMinimum:
		minDur, err := h.uceglpc.FailsafeDurationMinimum(entity)
		if err != nil {
			fmt.Println("Error getting FailsafeDurationMinimum:", err)
		} else {
			peer.usecaseData.LpcFailsafeDur = minDur / time.Minute
		}
	case eglpc.DataUpdateFailsafeConsumptionActivePowerLimit:
		powerLimit, err := h.uceglpc.FailsafeConsumptionActivePowerLimit(entity)
		if err != nil {
			fmt.Println("Error getting FailsafeConsumptionActivePowerLimit:", err)
		} else {
			peer.usecaseData.LpcFailsafePower = powerLimit
		}
	case eglpc.DataUpdateHeartbeat:
		peer.usecaseData.LpcHeartbeatOk = h.uceglpc.IsHeartbeatWithinDuration(entity)
		peer.usecaseData.LpcHeartbeatTimestamp = time.Now()
	default:
		nominal, err := h.uceglpc.ConsumptionNominalMax(entity)
		if err != nil {
			fmt.Println("Error getting ConsumptionNominalMax:", err)
		} else {
			peer.usecaseData.LpcConsumptionLimitNominalMax = nominal
		}
	}
	h.updateEntitiesFromDevice(ski, device, peer)
}

// HandleEgEvcc Energy Guard EVCC Handler
func (h *hems) HandleEgEvcc(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgEVCC Event: ", event)

	peer := h.getOrCreatePeer(ski)

	switch event {
	case cemevcc.UseCaseSupportUpdate:
		h.setUsecaseSupportedForPeer(peer, "EVCC", true)
	case cemevcc.DataUpdateManufacturerData:
		manufacturer, err := h.uccemevcc.ManufacturerData(entity)
		if err != nil {
			fmt.Println("Error getting ManufacturerData:", err)
		} else {
			peer.usecaseData.EvccManufacturerData = manufacturer
		}
	case cemevcc.DataUpdateChargeState:
		chargeState, err := h.uccemevcc.ChargeState(entity)
		if err != nil {
			fmt.Println("Error getting ChargeState:", err)
		} else {
			peer.usecaseData.EvccChargeState = string(chargeState)
		}
	case cemevcc.DataUpdateAsymmetricChargingSupport:
		support, err := h.uccemevcc.AsymmetricChargingSupport(entity)
		if err != nil {
			fmt.Println("Error getting AsymmetricChargingSupport:", err)
		} else {
			peer.usecaseData.EvccAsymmetricChargingSupport = support
		}
	case cemevcc.DataUpdateCommunicationStandard:
		standard, err := h.uccemevcc.CommunicationStandard(entity)
		if err != nil {
			fmt.Println("Error getting CommunicationStandard:", err)
		} else {
			peer.usecaseData.EvccCommunicationStandard = string(standard)
		}
	case cemevcc.DataUpdateCurrentLimits:
		minimum, maximum, standby, err := h.uccemevcc.ChargingPowerLimits(entity)
		if err != nil {
			fmt.Println("Error getting ChargingPowerLimits:", err, minimum, maximum, standby)
		} else {
			peer.usecaseData.EvccLimitMinimum = minimum
			peer.usecaseData.EvccLimitMaximum = maximum
			peer.usecaseData.EvccLimitStandby = standby
		}
	case cemevcc.DataUpdateIdentifications:
		identifications, err := h.uccemevcc.Identifications(entity)
		if err != nil {
			fmt.Println("Error getting Identifications:", err)
		} else {
			peer.usecaseData.EvccIdentifications = identifications
		}

	case cemevcc.DataUpdateIsInSleepMode:
		sleepMode, err := h.uccemevcc.IsInSleepMode(entity)
		if err != nil {
			fmt.Println("Error getting IsInSleepMode:", err)
		} else {
			peer.usecaseData.EvccSleepMode = sleepMode
		}
	case cemevcc.EvConnected:
		fmt.Println("EVCC Connected")
		peer.usecaseData.EvccEvConnected = true
	case cemevcc.EvDisconnected:
		fmt.Println("EVCC Disconnected")
		peer.usecaseData.EvccEvConnected = false
	}
	h.updateEntitiesFromDevice(ski, device, peer)
}

// HandleEgEvcem Energy Guard EVCEM Handler
func (h *hems) HandleEgEvcem(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgEVCEM Event: ", event)

	peer := h.getOrCreatePeer(ski)

	switch event {
	case cemevcem.UseCaseSupportUpdate:
		h.setUsecaseSupportedForPeer(peer, "EVCEM", true)
	case cemevcem.DataUpdateCurrentPerPhase:
		currentArray, err := h.uccemevcem.CurrentPerPhase(entity)
		if err != nil {
			fmt.Println("Error getting CurrentPerPhase:", err)
		} else {
			peer.usecaseData.EvcemCurrentPerPhase = currentArray
		}
	case cemevcem.DataUpdatePhasesConnected:
		phasesConnected, err := h.uccemevcem.PhasesConnected(entity)
		if err != nil {
			fmt.Println("Error getting PhasesConnected:", err)
		} else {
			peer.usecaseData.EvcemPhasesConnected = phasesConnected
		}
	case cemevcem.DataUpdateEnergyCharged:
		energyCharged, err := h.uccemevcem.EnergyCharged(entity)
		if err != nil {
			fmt.Println("Error getting EnergyCharged:", err)
		} else {
			peer.usecaseData.EvcemEnergyCharged = energyCharged
		}
	case cemevcem.DataUpdatePowerPerPhase:
		powerPerPhaseArray, err := h.uccemevcem.PowerPerPhase(entity)
		if err != nil {
			fmt.Println("Error getting PowerPerPhase:", err)
		} else {
			peer.usecaseData.EvcemPowerPerPhase = powerPerPhaseArray
		}
	}
	h.updateEntitiesFromDevice(ski, device, peer)
}

// HandleEgEvsecc Energy Guard EVSECC Handler
func (h *hems) HandleEgEvsecc(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgEVSECC Event: ", event)

	peer := h.getOrCreatePeer(ski)

	switch event {
	case cemevsecc.UseCaseSupportUpdate:
		h.setUsecaseSupportedForPeer(peer, "EVSECC", true)
	case cemevsecc.DataUpdateManufacturerData:
		manufacturer, err := h.uccemevsecc.ManufacturerData(entity)
		if err != nil {
			fmt.Println("Error getting ManufacturerData:", err)
		} else {
			peer.usecaseData.EvseccManufacturerData = manufacturer
		}
	case cemevsecc.DataUpdateOperatingState:
		operatingState, errorMessage, err := h.uccemevsecc.OperatingState(entity)
		if err != nil {
			fmt.Println("Error getting OperatingState:", err)
		} else {
			peer.usecaseData.EvseccOperatingState = string(operatingState)
			peer.usecaseData.EvseccOperatingStateDescription = errorMessage
		}
	}
	h.updateEntitiesFromDevice(ski, device, peer)
}

// HandleEgCevc Energy Guard CEVC Handler
func (h *hems) HandleEgCevc(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgCEVC Event: ", event)

	peer := h.getOrCreatePeer(ski)

	switch event {
	case cemcevc.UseCaseSupportUpdate:
		h.setUsecaseSupportedForPeer(peer, "CEVC", true)
	case cemcevc.DataUpdateEnergyDemand:
		demand, err := h.uccemcevc.EnergyDemand(entity)
		if err != nil {
			fmt.Println("Error getting EnergyDemand:", err)
		} else {
			peer.usecaseData.CevcEnergyDemand = demand
		}
		// Also update charge strategy when energy demand changes
		fmt.Println("cevc demand: ", demand)
		strategy := h.uccemcevc.ChargeStrategy(entity)
		peer.usecaseData.CevcChargeStrategy = string(strategy)
	case cemcevc.DataUpdateTimeSlotConstraints:
		constraints, err := h.uccemcevc.TimeSlotConstraints(entity)
		if err != nil {
			fmt.Println("Error getting TimeSlotConstraints:", err)
		} else {
			fmt.Println("TimeSlotConstraints: ", constraints)
			peer.usecaseData.CevcTimeSlotConstraints = constraints
		}
	case cemcevc.DataUpdateIncentiveTable:
		incentives, err := h.uccemcevc.IncentiveConstraints(entity)
		if err != nil {
			fmt.Println("Error getting IncentiveConstraints:", err)
		} else {
			fmt.Println("IncentiveConstraints: ", incentives)
			peer.usecaseData.CevcIncentiveConstraints = incentives
		}
	case cemcevc.DataUpdateChargePlanConstraints:
		planConstraints, err := h.uccemcevc.ChargePlanConstraints(entity)
		if err != nil {
			fmt.Println("Error getting ChargePlanConstraints:", err)
		} else {
			fmt.Println("ChargePlanConstraints: ", planConstraints)
			peer.usecaseData.CevcChargePlanConstraints = planConstraints
		}
	case cemcevc.DataUpdateChargePlan:
		plan, err := h.uccemcevc.ChargePlan(entity)
		if err != nil {
			fmt.Println("Error getting ChargePlan:", err)
		} else {
			fmt.Println("ChargePlan: ", plan)
			peer.usecaseData.CevcChargePlan = plan
		}
	case cemcevc.DataRequestedPowerLimitsAndIncentives:
		fmt.Println("CEVC: EV requested power limits and incentives - sending defaults")
		// Send default power limits (max possible for 7 days)
		err := h.uccemcevc.WritePowerLimits(entity, nil)
		if err != nil {
			fmt.Println("Error writing default PowerLimits:", err)
		}
		// Send default incentives (same price for 7 days)
		err = h.uccemcevc.WriteIncentives(entity, nil)
		if err != nil {
			fmt.Println("Error writing default Incentives:", err)
		}
	case cemcevc.DataRequestedIncentiveTableDescription:
		fmt.Println("CEVC: EV requested incentive table description")
		// This would require setting up tariff descriptions - using nil for defaults
		err := h.uccemcevc.WriteIncentiveTableDescriptions(entity, nil)
		if err != nil {
			fmt.Println("Error writing IncentiveTableDescriptions:", err)
		}
	}
	h.updateEntitiesFromDevice(ski, device, peer)
}

// HandleMaMpc MaMPC Handler

func (h *hems) HandleMaMpc(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("MaMpc Event: ", event)

	peer := h.getOrCreatePeer(ski)

	switch event {
	case mampc.UseCaseSupportUpdate:
		h.setUsecaseSupportedForPeer(peer, "MPC", true)
	case mampc.DataUpdatePowerPerPhase:
		power, err := h.ucmampc.PowerPerPhase(entity)
		if err != nil {
			fmt.Println("Error getting PowerPerPhase:", err)
		} else {
			peer.usecaseData.MpcPowerPerPhase = power
		}
	case mampc.DataUpdateCurrentsPerPhase:
		currents, err := h.ucmampc.CurrentPerPhase(entity)
		if err != nil {
			fmt.Println("Error getting CurrentPerPhase:", err)
		} else {
			peer.usecaseData.MpcCurrentPerPhase = currents
		}
	case mampc.DataUpdatePower:
		power, err := h.ucmampc.Power(entity)
		if err != nil {
			fmt.Println("Error getting Power:", err)
		} else {
			peer.usecaseData.MpcPower = power
		}
	case mampc.DataUpdateEnergyConsumed:
		consumed, err := h.ucmampc.EnergyConsumed(entity)
		if err != nil {
			fmt.Println("Error getting EnergyConsumed:", err)
		} else {
			peer.usecaseData.MpcEnergyConsumed = consumed
		}
	case mampc.DataUpdateEnergyProduced:
		produced, err := h.ucmampc.EnergyProduced(entity)
		if err != nil {
			fmt.Println("Error getting EnergyProduced:", err)
		} else {
			peer.usecaseData.MpcEnergyProduced = produced
		}
	case mampc.DataUpdateFrequency:
		frequency, err := h.ucmampc.Frequency(entity)
		if err != nil {
			fmt.Println("Error getting Frequency:", err)
		} else {
			peer.usecaseData.MpcFrequency = frequency
		}
	case mampc.DataUpdateVoltagePerPhase:
		voltages, err := h.ucmampc.VoltagePerPhase(entity)
		if err != nil {
			fmt.Println("Error getting VoltagePerPhase:", err)
		} else {
			peer.usecaseData.MpcVoltagePerPhase = voltages
		}
	}
	h.updateEntitiesFromDevice(ski, device, peer)
}

// HandleMaMGCP MaMGCP Handler (Monitoring of Grid Connection Point)
func (h *hems) HandleMaMGCP(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("MaMGCP Event: ", event)

	peer := h.getOrCreatePeer(ski)

	switch event {
	case mamgrp.UseCaseSupportUpdate:
		h.setUsecaseSupportedForPeer(peer, "MGCP", true)
	case mamgrp.DataUpdatePowerLimitationFactor:
		factor, err := h.ucmamgrp.PowerLimitationFactor(entity)
		if err != nil {
			fmt.Println("Error getting PowerLimitationFactor:", err)
		} else {
			peer.usecaseData.MgcPowerLimitationFactor = factor
		}
	case mamgrp.DataUpdatePower:
		power, err := h.ucmamgrp.Power(entity)
		if err != nil {
			fmt.Println("Error getting MGCP Power:", err)
		} else {
			peer.usecaseData.MgcPower = power
		}
	case mamgrp.DataUpdateEnergyFeedIn:
		feedIn, err := h.ucmamgrp.EnergyFeedIn(entity)
		if err != nil {
			fmt.Println("Error getting EnergyFeedIn:", err)
		} else {
			peer.usecaseData.MgcEnergyFeedIn = feedIn
		}
	case mamgrp.DataUpdateEnergyConsumed:
		consumed, err := h.ucmamgrp.EnergyConsumed(entity)
		if err != nil {
			fmt.Println("Error getting MGCP EnergyConsumed:", err)
		} else {
			peer.usecaseData.MgcEnergyConsumed = consumed
		}
	case mamgrp.DataUpdateCurrentPerPhase:
		currents, err := h.ucmamgrp.CurrentPerPhase(entity)
		if err != nil {
			fmt.Println("Error getting MGCP CurrentPerPhase:", err)
		} else {
			peer.usecaseData.MgcCurrentPerPhase = currents
		}
	case mamgrp.DataUpdateVoltagePerPhase:
		voltages, err := h.ucmamgrp.VoltagePerPhase(entity)
		if err != nil {
			fmt.Println("Error getting MGCP VoltagePerPhase:", err)
		} else {
			peer.usecaseData.MgcVoltagePerPhase = voltages
		}
	case mamgrp.DataUpdateFrequency:
		frequency, err := h.ucmamgrp.Frequency(entity)
		if err != nil {
			fmt.Println("Error getting MGCP Frequency:", err)
		} else {
			peer.usecaseData.MgcFrequency = frequency
		}
	}
	h.updateEntitiesFromDevice(ski, device, peer)
}

func (h *hems) HandleCemOpev(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("CemOpev Event: ", event)

	peer := h.getOrCreatePeer(ski)

	switch event {
	case cemopev.UseCaseSupportUpdate:
		h.setUsecaseSupportedForPeer(peer, "OPEV", true)
	case cemopev.DataUpdateLimit:
		loadlimit, err := h.uccemopev.LoadControlLimits(entity)
		if err != nil {
			fmt.Println("Error getting LoadControlLimits:", err)
		} else {
			fmt.Println("LoadControlLimits:", loadlimit)
			peer.usecaseData.OpevLoadControlLimit = loadlimit
		}
	case cemopev.DataUpdateCurrentLimits:
		currentlimitMin, currentlimitMax, currentlimitDefault, err := h.uccemopev.CurrentLimits(entity)
		if err != nil {
			fmt.Println("Error getting CurrentLimits:", err)
		} else {
			fmt.Println("CurrentLimits:", currentlimitMin, currentlimitMax, currentlimitDefault)
			peer.usecaseData.OpevCurrentLimitMin = currentlimitMin
			peer.usecaseData.OpevCurrentLimitMax = currentlimitMax
			peer.usecaseData.OpevCurrentLimitDefault = currentlimitDefault
		}
	}
	h.updateEntitiesFromDevice(ski, device, peer)

}

// HandleCemOscev CEM OSCEV Handler (Optimization of Self-Consumption During EV Charging)
func (h *hems) HandleCemOscev(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("CemOscev Event: ", event)

	peer := h.getOrCreatePeer(ski)

	switch event {
	case cemoscev.UseCaseSupportUpdate:
		h.setUsecaseSupportedForPeer(peer, "OSCEV", true)
	case cemoscev.DataUpdateLimit:
		loadlimit, err := h.uccemoscev.LoadControlLimits(entity)
		if err != nil {
			fmt.Println("Error getting OSCEV LoadControlLimits:", err)
		} else {
			peer.usecaseData.OscevLoadControlLimit = loadlimit
		}
	case cemoscev.DataUpdateCurrentLimits:
		currentlimitMin, currentlimitMax, currentlimitDefault, err := h.uccemoscev.CurrentLimits(entity)
		if err != nil {
			fmt.Println("Error getting OSCEV CurrentLimits:", err)
		} else {
			peer.usecaseData.OscevCurrentLimitMin = currentlimitMin
			peer.usecaseData.OscevCurrentLimitMax = currentlimitMax
			peer.usecaseData.OscevCurrentLimitDefault = currentlimitDefault
		}
	}
	h.updateEntitiesFromDevice(ski, device, peer)
}

// HandleCemEvsoc CEM EVSOC Handler (EV State Of Charge)
func (h *hems) HandleCemEvsoc(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("CemEvsoc Event: ", event)

	peer := h.getOrCreatePeer(ski)

	switch event {
	case cemevsoc.UseCaseSupportUpdate:
		h.setUsecaseSupportedForPeer(peer, "EVSOC", true)
	case cemevsoc.DataUpdateStateOfCharge:
		soc, err := h.uccemevsoc.StateOfCharge(entity)
		if err != nil {
			fmt.Println("Error getting StateOfCharge:", err)
		} else {
			peer.usecaseData.EvsocStateOfCharge = soc
		}
	}
	h.updateEntitiesFromDevice(ski, device, peer)
}

// Write Functions

func (h *hems) WriteLPCConsumptionLimit(durationSeconds int64, value float64, active bool) error {
	// iterate remote entities and write the provided consumption limit
	entities := h.uceglpc.RemoteEntitiesScenarios()

	fmt.Println("Writing LPC Consumption Limit:", durationSeconds, value, active)
	fmt.Println("Found entities:", entities)
	var errs []string
	for _, entity := range entities {
		_, err := h.uceglpc.WriteConsumptionLimit(entity.Entity, ucapi.LoadLimit{
			Duration:     time.Duration(durationSeconds) * time.Second,
			IsChangeable: false,
			IsActive:     active,
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

func (h *hems) WriteLPCFailsafeDuration(minDuration time.Duration) {
	// iterate remote entities and write the failsafe duration
	entities := h.uceglpc.RemoteEntitiesScenarios()
	fmt.Println("Writing LPC Failsafe Duration:", minDuration)
	fmt.Println("Found entities:", entities)
	for _, entity := range entities {
		_, err := h.uceglpc.WriteFailsafeDurationMinimum(entity.Entity, minDuration)
		if err != nil {
			fmt.Println("Error writing failsafeDurationMinimum:", err)
		} else {
			fmt.Println("Wrote failsafeDurationMinimum to entity", entity)
		}
	}
}
func (h *hems) WriteLPCFailsafeValue(failsafePowerLimit float64) {
	// iterate remote entities and write the failsafe power limit
	entities := h.uceglpc.RemoteEntitiesScenarios()
	fmt.Println("Writing LPC Failsafe Power Limit:", failsafePowerLimit)
	fmt.Println("Found entities:", entities)
	for _, entity := range entities {
		_, err := h.uceglpc.WriteFailsafeConsumptionActivePowerLimit(entity.Entity, failsafePowerLimit)
		if err != nil {
			fmt.Println("Error writing FailsafeConsumptionActivePowerLimit:", err)
		} else {
			fmt.Println("Wrote FailsafeConsumptionActivePowerLimit to entity", entity)
		}
	}
}

func (h *hems) WriteLPPProductionLimit(durationSeconds int64, value float64, active bool) error {
	// Ensure the value is always negative for Production Limits (LPP)
	// per EEBus sign convention: negative values limit production.
	forcedNegativeValue := -math.Abs(value)

	// iterate remote entities and write the provided production limit
	entities := h.uceglpp.RemoteEntitiesScenarios()

	fmt.Println("Writing LPP Production Limit:", durationSeconds, forcedNegativeValue, active)

	limit := ucapi.LoadLimit{
		Duration: time.Duration(durationSeconds) * time.Second,
		IsActive: active,
		Value:    forcedNegativeValue,
	}

	fmt.Println("Found entities:", entities)
	var errs []string
	resultCB := func(msg model.ResultDataType) {
		if *msg.ErrorNumber == model.ErrorNumberTypeNoError {
			fmt.Println("Production limit accepted.")
		} else {
			fmt.Println("Production limit rejected. Code", *msg.ErrorNumber, "Description", *msg.Description)
		}
	}

	for _, entity := range entities {
		_, err := h.uceglpp.WriteProductionLimit(entity.Entity, limit, resultCB)
		if err != nil {
			errStr := fmt.Sprintf("%v: %v", entity, err)
			errs = append(errs, errStr)
			fmt.Println("Error writing production limit:", err)
		} else {
			fmt.Println("Wrote production limit to entity", entity)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (h *hems) WriteLPPFailsafeDuration(minDuration time.Duration) {
	// iterate remote entities and write the failsafe duration
	entities := h.uceglpp.RemoteEntitiesScenarios()
	fmt.Println("Writing LPP Failsafe Duration:", minDuration)
	fmt.Println("Found entities:", entities)
	for _, entity := range entities {
		_, err := h.uceglpp.WriteFailsafeDurationMinimum(entity.Entity, minDuration)
		if err != nil {
			fmt.Println("Error writing failsafeDurationMinimum:", err)
		} else {
			fmt.Println("Wrote failsafeDurationMinimum to entity", entity)
		}
	}
}

func (h *hems) WriteLPPFailsafeValue(failsafePowerLimit float64) {
	// iterate remote entities and write the failsafe power limit
	entities := h.uceglpp.RemoteEntitiesScenarios()
	fmt.Println("Writing LPP Failsafe Power Limit:", failsafePowerLimit)
	fmt.Println("Found entities:", entities)
	for _, entity := range entities {
		_, err := h.uceglpp.WriteFailsafeProductionActivePowerLimit(entity.Entity, failsafePowerLimit)
		if err != nil {
			fmt.Println("Error writing FailsafeProductionActivePowerLimit:", err)
		} else {
			fmt.Println("Wrote FailsafeProductionActivePowerLimit to entity", entity)
		}
	}
}

// OSCEV Write Functions

func (h *hems) WriteOSCEVLoadControlLimits(limits []ucapi.LoadLimitsPhase) error {
	entities := h.uccemoscev.RemoteEntitiesScenarios()
	fmt.Println("Writing OSCEV Load Control Limits:", limits)
	fmt.Println("Found entities:", entities)
	var errs []string
	for _, entity := range entities {
		_, err := h.uccemoscev.WriteLoadControlLimits(entity.Entity, limits, nil)
		if err != nil {
			errStr := fmt.Sprintf("%v: %v", entity, err)
			errs = append(errs, errStr)
			fmt.Println("Error writing OSCEV LoadControlLimits:", err)
		} else {
			fmt.Println("Wrote OSCEV LoadControlLimits to entity", entity)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// WriteOPEVLoadControlLimits sends load control limits to OPEV entities
func (h *hems) WriteOPEVLoadControlLimits(limits []ucapi.LoadLimitsPhase) error {
	entities := h.uccemopev.RemoteEntitiesScenarios()
	fmt.Println("Writing OPEV Load Control Limits:", limits)
	fmt.Println("Found entities:", entities)
	var errs []string
	for _, entity := range entities {
		_, err := h.uccemopev.WriteLoadControlLimits(entity.Entity, limits, nil)
		if err != nil {
			errStr := fmt.Sprintf("%v: %v", entity, err)
			errs = append(errs, errStr)
			fmt.Println("Error writing OPEV LoadControlLimits:", err)
		} else {
			fmt.Println("Wrote OPEV LoadControlLimits to entity", entity)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// EEBUSServiceHandler

func (h *hems) RemoteSKIConnected(service api.ServiceInterface, ski string) {
	fmt.Printf("Remote SKI connected: %s\n", ski)
	peer := h.getOrCreatePeer(ski)
	peer.connected = true
	peer.lastSeen = time.Now()
	h.broadcastPeerList()
}

func (h *hems) RemoteSKIDisconnected(service api.ServiceInterface, ski string) {
	fmt.Printf("Remote SKI disconnected: %s\n", ski)
	peer := h.getPeer(ski)
	if peer != nil {
		peer.connected = false
		h.broadcastPeerList()
	}
}

func (h *hems) VisibleRemoteServicesUpdated(service api.ServiceInterface, entries []shipapi.RemoteService) {
	fmt.Printf("Visible remote services updated: %d services\n", len(entries))

	// Track discovered services
	for _, entry := range entries {
		peer := h.getOrCreatePeer(entry.Ski)
		// Store device info from mDNS discovery
		peer.deviceName = entry.Name
		peer.brand = entry.Brand
		peer.model = entry.Model
		peer.deviceType = entry.Type
		peer.serial = entry.Serial
		peer.identifier = entry.Identifier
		peer.connected = false // Will be updated when actually connected
	}

	// Broadcast peer list to frontend
	h.broadcastPeerList()
}

// broadcastPeerList sends the current peer list to all WebSocket clients
func (h *hems) broadcastPeerList() {
	h.peersMu.Lock()
	peers := make([]PeerInfo, 0, len(h.peers))
	for ski, peer := range h.peers {
		info := PeerInfo{
			SKI:        ski,
			Connected:  peer.connected,
			LastSeen:   peer.lastSeen,
			Usecases:   make(map[string]bool),
			DeviceName: peer.deviceName,
			Brand:      peer.brand,
			Model:      peer.model,
			DeviceType: peer.deviceType,
			Serial:     peer.serial,
			Identifier: peer.identifier,
		}
		for uc, supported := range peer.usecaseState {
			info.Usecases[uc] = supported
		}
		peers = append(peers, info)
	}
	h.peersMu.Unlock()

	msg := map[string]interface{}{
		"type":  "peers",
		"peers": peers,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		h.Errorf("marshal peer list: %v", err)
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

func (h *hems) ServiceShipIDUpdate(ski string, shipdID string) {}

func (h *hems) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	fmt.Printf("Pairing detail update for %s: state=%v\n", ski, detail.State())

	if detail.State() == shipapi.ConnectionStateRemoteDeniedTrust {
		fmt.Printf("The remote service %s denied trust.\n", ski)
		h.myService.CancelPairingWithSKI(ski)
		h.myService.UnregisterRemoteSKI(ski)
		// Don't exit - just log the error for this peer
		// The application continues running for other peers
	}
}

func (h *hems) AllowWaitingForTrust(ski string) bool {
	// Allow waiting for trust for all SKIs initially
	return true
}

// UCEvseCommisioningConfigurationCemDelegate

// handle device state updates from the remote EVSE device
func (h *hems) HandleEVSEDeviceState(ski string, failure bool, errorCode string) {
	fmt.Println("EVSE Error State:", failure, errorCode)
}

// main app
func usage() {
	fmt.Println("Usage:")
	fmt.Println("  ./device-tester <serverport> [<crtfile> <keyfile>]")
	fmt.Println()
	fmt.Println("The tool will automatically discover EEBUS devices on the network.")
	fmt.Println("Use the web interface at http://localhost:8080 to view and connect to peers.")
	fmt.Println()
	fmt.Println("If cert.pem and key.pem are available in the exe directory, they will be used automatically.")
	fmt.Println("Otherwise, a new self-signed certificate will be created and stored there.")
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

	// Try to extract SKI from the trace message for routing
	ski := h.extractSKIFromMessage(value)

	// broadcast (append to logs / send to WS)
	ts := h.currentTimestamp()
	line := fmt.Sprintf("%s TRACE %s %s", ts, ski, value)
	h.appendLog(strings.TrimRight(line, "\n"))
	// still print to stdout if enabled
	if h.config.Logging.EnableTrace {
		fmt.Printf("%s", line)
	}
}

func (h *hems) Tracef(format string, args ...interface{}) {
	// Always broadcast formatted trace to frontend
	value := fmt.Sprintf(format, args...)

	// Try to extract SKI from the message
	ski := h.extractSKIFromMessage(value)

	ts := h.currentTimestamp()
	line := fmt.Sprintf("%s TRACEF %s %s", ts, ski, value)
	h.appendLog(strings.TrimRight(line, "\n"))
	if h.config.Logging.EnableTrace {
		fmt.Println(line)
	}
}

func (h *hems) Debug(args ...interface{}) {
	// Always broadcast debug messages to frontend
	value := fmt.Sprintln(args...)

	// Try to extract SKI from the message
	ski := h.extractSKIFromMessage(value)

	ts := h.currentTimestamp()
	line := fmt.Sprintf("%s DEBUG %s %s", ts, ski, value)
	h.appendLog(strings.TrimRight(line, "\n"))
	if h.config.Logging.EnableDebug {
		fmt.Printf("%s", line)
		if strings.Contains(line, "operation is not supported") || strings.Contains(line, "data not available") {
			debug.PrintStack()
		}
	}
}

func (h *hems) Debugf(format string, args ...interface{}) {
	// Always broadcast formatted debug messages to frontend
	value := fmt.Sprintf(format, args...)

	// Try to extract SKI from the message
	ski := h.extractSKIFromMessage(value)

	ts := h.currentTimestamp()
	line := fmt.Sprintf("%s DEBUGF %s %s", ts, ski, value)
	h.appendLog(strings.TrimRight(line, "\n"))
	if h.config.Logging.EnableDebug {
		fmt.Println(line)
		if strings.Contains(line, "operation is not supported") {
			debug.PrintStack()
		}
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
	debug.PrintStack()
}

func (h *hems) Errorf(format string, args ...interface{}) {
	h.printFormat("ERRORF", format, args...)
	debug.PrintStack()
}

// extractSKIFromMessage attempts to extract an SKI from a log message
// Returns the SKI if found, empty string otherwise
func (h *hems) extractSKIFromMessage(msg string) string {
	// SKI format is typically 64 hex characters
	// Look for patterns that might indicate an SKI

	// Common patterns in EEBUS messages:
	// - "ski:<SKI>"
	// - "[SKI]"
	// - SKI in path or identifier

	// Check for known SKIs from our peer list
	h.peersMu.Lock()
	defer h.peersMu.Unlock()

	for ski := range h.peers {
		if strings.Contains(msg, ski) {
			return ski
		}
	}

	// Try to find a 64-character hex string (typical SKI length)
	words := strings.Fields(msg)
	for _, word := range words {
		// Remove common delimiters
		word = strings.Trim(word, "[]():;,.\"")
		if len(word) == 64 {
			// Check if it's valid hex
			isHex := true
			for _, c := range word {
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
					isHex = false
					break
				}
			}
			if isHex {
				return word
			}
		}
	}

	return ""
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

	// Try to extract SKI from the message
	ski := h.extractSKIFromMessage(value)

	ts := h.currentTimestamp()
	line := fmt.Sprintf("%s %s %s %s", ts, msgType, ski, value)
	fmt.Printf("%s", line)
	// also store in in-memory buffer
	h.appendLog(strings.TrimRight(line, "\n"))
}

func (h *hems) printFormat(msgType, format string, args ...interface{}) {
	value := fmt.Sprintf(format, args...)

	// Try to extract SKI from the message
	ski := h.extractSKIFromMessage(value)

	ts := h.currentTimestamp()
	line := fmt.Sprintf("%s %s %s %s", ts, msgType, ski, value)
	fmt.Println(line)
	h.appendLog(line)
}

// setUsecaseSupported updates the global state and broadcasts the change
func (h *hems) setUsecaseSupported(name string, supported bool) {
	ucMu.Lock()
	defer ucMu.Unlock()

	// Update global state
	if old, ok := h.globalUseCaseState[name]; ok && old == supported {
		return
	}
	h.globalUseCaseState[name] = supported

	// Update all peers
	h.peersMu.Lock()
	for _, peer := range h.peers {
		peer.usecaseState[name] = supported
	}
	h.peersMu.Unlock()

	// Broadcast per-peer usecase messages to websocket clients so frontend
	// receives updates including the peer SKI (multi-peer aware)
	h.wsMu.Lock()
	defer h.wsMu.Unlock()
	for ski := range h.peers {
		msg := map[string]interface{}{"type": "usecase", "name": name, "supported": supported, "ski": ski}
		b, err := json.Marshal(msg)
		if err != nil {
			h.Errorf("marshal usecase update: %v", err)
			continue
		}
		for c := range h.wsConns {
			if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
				c.Close()
				delete(h.wsConns, c)
			}
		}
	}
}

// setUsecaseSupportedForPeer sets usecase support for a specific peer
func (h *hems) setUsecaseSupportedForPeer(peer *peerData, name string, supported bool) {
	if peer == nil {
		return
	}

	peer.usecaseState[name] = supported

	// Also update global state
	ucMu.Lock()
	h.globalUseCaseState[name] = supported
	ucMu.Unlock()

	// broadcast a json message to websocket clients
	msg := map[string]interface{}{
		"type":      "usecase",
		"name":      name,
		"supported": supported,
		"ski":       peer.ski,
	}
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

// updateEntitiesFromDevice updates the entities for a specific peer
func (h *hems) updateEntitiesFromDevice(ski string, device spineapi.DeviceRemoteInterface, peer *peerData) {
	if peer == nil || device == nil {
		return
	}

	// update peer's entities slice
	peer.entities = device.Entities()

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
	for _, e := range peer.entities {
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
	msg := map[string]interface{}{
		"type":     "entities",
		"ski":      ski,
		"entities": out,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		h.Errorf("marshal entities: %v", err)
		return
	}

	// cache last entities json for this peer
	peer.lastEntitiesJSON = b

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

	webAddr := "localhost"
	if v := os.Getenv("WEB_ADDR"); v != "" {
		webAddr = v
	}

	// initialize wsConns map
	h.wsMu.Lock()
	h.wsConns = make(map[*websocket.Conn]struct{})
	h.wsMu.Unlock()

	// determine executable directory (used as base for web assets)
	exePath, err := os.Executable()
	if err != nil {
		exePath = "."
	}
	exePath = filepath.Dir(exePath)

	// We deliberately read static assets from disk on every request and
	// set headers to prevent any caching in browser or in the program.
	// This keeps the UI editable during development without restart.

	// index handler: read `web/index.html` from disk on every request
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// no-cache headers for browser
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")

		indexPath := filepath.Join(exePath, "web", "index.html")
		data, err := os.ReadFile(indexPath)
		if err != nil {
			h.Errorf("failed to read web template %s: %v", indexPath, err)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("internal error"))
			return
		}
		if _, err := w.Write(data); err != nil {
			h.Errorf("write index.html: %v", err)
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

		// also send current global usecase state snapshot
		ucMu.Lock()
		for name, supported := range h.globalUseCaseState {
			msg := map[string]interface{}{"type": "usecase", "name": name, "supported": supported}
			if b, err := json.Marshal(msg); err == nil {
				_ = c.WriteMessage(websocket.TextMessage, b)
			}
		}
		ucMu.Unlock()

		// send current peer list
		h.broadcastPeerList()

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
		case "writeLPCConsumptionLimit":
			// expect: durationSeconds (int), value (float), isActive (bool)
			var durSec int64
			var val float64
			var isActive bool
			if d, ok := payload["durationSeconds"].(float64); ok {
				durSec = int64(d)
			}
			if v, ok := payload["value"].(float64); ok {
				val = v
			}
			if a, ok := payload["isActive"].(bool); ok {
				isActive = a
			}
			if err := h.WriteLPCConsumptionLimit(durSec, val, isActive); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(err.Error()))
				return
			}
			_, _ = w.Write([]byte("ok"))
			return
		case "writeLPCFailsafeDuration":
			// expect: durationMinutes (int)
			var minutes int64
			if d, ok := payload["durationMinutes"].(float64); ok {
				minutes = int64(d)
			}
			minDuration := time.Duration(minutes) * time.Minute
			h.WriteLPCFailsafeDuration(minDuration)
			_, _ = w.Write([]byte("ok"))
			return
		case "writeLPCFailsafeValue":
			// expect: failsafePower (float)
			var limit float64
			if l, ok := payload["failsafePower"].(float64); ok {
				limit = l
			}
			h.WriteLPCFailsafeValue(limit)
			_, _ = w.Write([]byte("ok"))
			return
		case "writeLPPProductionLimit":
			// expect: durationSeconds (int), value (float), isActive (bool)
			var durSec int64
			var val float64
			var isActive bool
			if d, ok := payload["durationSeconds"].(float64); ok {
				durSec = int64(d)
			}
			if v, ok := payload["value"].(float64); ok {
				val = v
			}
			if a, ok := payload["isActive"].(bool); ok {
				isActive = a
			}
			if err := h.WriteLPPProductionLimit(durSec, val, isActive); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(err.Error()))
				return
			}
			_, _ = w.Write([]byte("ok"))
			return
		case "writeLPPFailsafeDuration":
			// expect: durationMinutes (int)
			var minutes int64
			if d, ok := payload["durationMinutes"].(float64); ok {
				minutes = int64(d)
			}
			minDuration := time.Duration(minutes) * time.Minute
			h.WriteLPPFailsafeDuration(minDuration)
			_, _ = w.Write([]byte("ok"))
			return
		case "writeLPPFailsafeValue":
			// expect: failsafePower (float)
			var limit float64
			if l, ok := payload["failsafePower"].(float64); ok {
				limit = l
			}
			h.WriteLPPFailsafeValue(limit)
			_, _ = w.Write([]byte("ok"))
			return
		case "writeOSCEVLoadControlLimits":
			// Simple interface: accept value and isActive, build limits array for all phases
			value, ok := payload["value"].(float64)
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("value must be a number"))
				return
			}
			isActive, ok := payload["isActive"].(bool)
			if !ok {
				isActive = true // default to active
			}
			// Build limits for all three phases
			limits := []ucapi.LoadLimitsPhase{
				{Phase: model.ElectricalConnectionPhaseNameTypeA, Value: value, IsActive: isActive},
				{Phase: model.ElectricalConnectionPhaseNameTypeB, Value: value, IsActive: isActive},
				{Phase: model.ElectricalConnectionPhaseNameTypeC, Value: value, IsActive: isActive},
			}
			if err := h.WriteOSCEVLoadControlLimits(limits); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(err.Error()))
				return
			}
			_, _ = w.Write([]byte("ok"))
			return
		case "writeOPEVLoadControlLimits":
			// Simple interface: accept value and isActive, build limits array for all phases
			value, ok := payload["value"].(float64)
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("value must be a number"))
				return
			}
			isActive, ok := payload["isActive"].(bool)
			if !ok {
				isActive = true // default to active
			}
			// Build limits for all three phases
			limits := []ucapi.LoadLimitsPhase{
				{Phase: model.ElectricalConnectionPhaseNameTypeA, Value: value, IsActive: isActive},
				{Phase: model.ElectricalConnectionPhaseNameTypeB, Value: value, IsActive: isActive},
				{Phase: model.ElectricalConnectionPhaseNameTypeC, Value: value, IsActive: isActive},
			}
			if err := h.WriteOPEVLoadControlLimits(limits); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(err.Error()))
				return
			}
			_, _ = w.Write([]byte("ok"))
			return
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("unknown command"))
			return
		}
	})

	// endpoint: return usecaseData (current values) in JSON-friendly units
	// Updated to support ski parameter for specific peer
	http.HandleFunc("/api/usecasedata", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		ski := r.URL.Query().Get("ski")
		if ski == "" {
			// No SKI specified, return empty or first peer's data
			// For backward compatibility, return first connected peer's data if available
			h.peersMu.Lock()
			for _, peer := range h.peers {
				if peer.connected {
					if err := json.NewEncoder(w).Encode(peer.usecaseData); err != nil {
						h.Errorf("encode usecasedata: %v", err)
					}
					h.peersMu.Unlock()
					return
				}
			}
			h.peersMu.Unlock()
			// No connected peers, return empty usecaseData
			if err := json.NewEncoder(w).Encode(usecaseData{}); err != nil {
				h.Errorf("encode usecasedata: %v", err)
			}
			return
		}

		peer := h.getPeer(ski)
		if peer == nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "peer not found"})
			return
		}

		if err := json.NewEncoder(w).Encode(peer.usecaseData); err != nil {
			h.Errorf("encode usecasedata: %v", err)
		}
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
		ucMu.Lock()
		defer ucMu.Unlock()
		type UC struct {
			Name      string `json:"name"`
			Supported bool   `json:"supported"`
		}
		var out []UC
		for name, supported := range h.globalUseCaseState {
			out = append(out, UC{Name: name, Supported: supported})
		}
		if err := json.NewEncoder(w).Encode(out); err != nil {
			h.Errorf("encode usecases: %v", err)
		}
	})

	// new endpoint: return entities for a specific peer
	http.HandleFunc("/api/entities", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		ski := r.URL.Query().Get("ski")
		if ski == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "ski parameter required"})
			return
		}

		peer := h.getPeer(ski)
		if peer == nil || peer.lastEntitiesJSON == nil {
			// return empty array
			_, _ = w.Write([]byte("[]"))
			return
		}

		_, _ = w.Write(peer.lastEntitiesJSON)
	})

	// new endpoint: return all discovered peers
	http.HandleFunc("/api/peers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		h.peersMu.Lock()
		peers := make([]PeerInfo, 0, len(h.peers))
		for ski, peer := range h.peers {
			info := PeerInfo{
				SKI:        ski,
				Connected:  peer.connected,
				LastSeen:   peer.lastSeen,
				Usecases:   make(map[string]bool),
				DeviceName: peer.deviceName,
				Brand:      peer.brand,
				Model:      peer.model,
				DeviceType: peer.deviceType,
				Serial:     peer.serial,
				Identifier: peer.identifier,
			}
			for uc, supported := range peer.usecaseState {
				info.Usecases[uc] = supported
			}
			peers = append(peers, info)
		}
		h.peersMu.Unlock()

		if err := json.NewEncoder(w).Encode(peers); err != nil {
			h.Errorf("encode peers: %v", err)
		}
	})

	// new endpoint: connect to a discovered peer
	http.HandleFunc("/api/connect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var payload struct {
			SKI string `json:"ski"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
			return
		}

		if payload.SKI == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "ski parameter required"})
			return
		}

		// Register the remote SKI to initiate connection
		h.myService.RegisterRemoteSKI(payload.SKI, "")

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]string{"status": "connecting", "ski": payload.SKI})
	})

	// new endpoint: return config to frontend
	http.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(h.config); err != nil {
			h.Errorf("encode config: %v", err)
		}
	})

	// Serve static /web assets from disk on every request with no-cache headers.
	fsDir := filepath.Join(exePath, "web")
	http.HandleFunc("/web/", func(w http.ResponseWriter, r *http.Request) {
		// set no-cache headers for browser
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")

		// derive relative path under fsDir
		rel := strings.TrimPrefix(r.URL.Path, "/web/")
		if rel == "" {
			// default to index.html inside web
			rel = "index.html"
		}
		// clean the path to prevent traversal
		rel = filepath.Clean(rel)
		filePath := filepath.Join(fsDir, rel)
		// ensure the resulting path is still under fsDir
		absFsDir, err := filepath.Abs(fsDir)
		if err != nil {
			h.Errorf("abs fsDir: %v", err)
			http.NotFound(w, r)
			return
		}
		absFilePath, err := filepath.Abs(filePath)
		if err != nil {
			h.Errorf("abs filePath: %v", err)
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(absFilePath, absFsDir) {
			h.Errorf("attempted path traversal: %s", filePath)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// serve file directly from disk (reads on each request)
		info, err := os.Stat(absFilePath)
		if err != nil {
			h.Debugf("static file not found: %s: %v", absFilePath, err)
			http.NotFound(w, r)
			return
		}
		if info.IsDir() {
			indexPath := filepath.Join(absFilePath, "index.html")
			if _, err := os.Stat(indexPath); err == nil {
				http.ServeFile(w, r, indexPath)
				return
			}
			http.NotFound(w, r)
			return
		}

		http.ServeFile(w, r, absFilePath)
	})

	addr := fmt.Sprintf("%s:%d", webAddr, webPort)
	h.Infof("Starting web interface on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		h.Errorf("web interface stopped: %v", err)
	}
}
