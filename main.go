package main

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
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
	//mampc "github.com/enbility/eebus-go/usecases/ma/mpc"

	shipapi "github.com/enbility/ship-go/api"
	"github.com/enbility/ship-go/cert"
	spineapi "github.com/enbility/spine-go/api"
	"github.com/enbility/spine-go/model"
)

var remoteSki string
var enableDebugLogging = false
var enableTraceLogging = false

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

	// usecase data
	usecaseData usecaseData
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
	h.uceglpc.UpdateUseCaseAvailability(false)
	h.myService.AddUseCase(h.uceglpc)
	h.setUsecaseSupported("LPC", false)

	// LPP
	h.uceglpp = eglpp.NewLPP(localEntity, h.HandleEgLPP)
	h.setUsecaseSupported("LPP", false)

	// MPC
	//h.ucmampc = mampc.NewMPC(localEntity, h.HandleMaMpc)
	//h.myService.AddUseCase(h.ucmampc)
	//h.setUsecaseSupported("MPC", false)

	if len(remoteSki) == 0 {
		os.Exit(0)
	}

	h.myService.RegisterRemoteSKI(remoteSki, "")

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
	switch event {
	case eglpp.UseCaseSupportUpdate:
		h.setUsecaseSupported("LPP", true)
	case eglpp.DataUpdateFailsafeDurationMinimum:
		minDur, err := h.uceglpp.FailsafeDurationMinimum(entity)
		if err != nil {
			fmt.Println("Error getting FailsafeDurationMinimum:", err)
		} else {
			h.usecaseData.LppFailsafeDur = minDur
		}
	case eglpp.DataUpdateFailsafeProductionActivePowerLimit:
		powerLimit, err := h.uceglpp.FailsafeProductionActivePowerLimit(entity)
		if err != nil {
			fmt.Println("Error getting FailsafeConsumptionActivePowerLimit:", err)
		} else {
			h.usecaseData.LppFailsafeValue = powerLimit
		}
	case eglpp.DataUpdateLimit:
		limit, err := h.uceglpp.ProductionLimit(entity)
		if err != nil {
			fmt.Println("Error getting ProductionNominalMax:", err)
		} else {
			h.usecaseData.LppLimitValue = limit.Value
			h.usecaseData.LppLimitDuration = limit.Duration / time.Second
			h.usecaseData.LppLimitActive = limit.IsActive
		}
	case eglpp.DataUpdateHeartbeat:
		h.usecaseData.LppHeartbeatOk = h.uceglpp.IsHeartbeatWithinDuration(entity)
		h.usecaseData.LppHeartbeatTimestamp = time.Now()
	}
	h.updateEntitiesFromDevice(device)
}

// HandleEgLPC Energy Guard LPC Handler
func (h *hems) HandleEgLPC(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgLPC Event: ", event)
	switch event {
	case eglpc.UseCaseSupportUpdate:
		h.setUsecaseSupported("LPC", true)
	case eglpc.DataUpdateLimit:
		limit, err := h.uceglpc.ConsumptionLimit(entity)
		if err != nil {
			fmt.Println("Error getting ConsumptionNominalMax:", err)
		} else {
			h.usecaseData.LpcLimitActive = limit.IsActive
			h.usecaseData.LpcLimitDurSeconds = limit.Duration / time.Second
			h.usecaseData.LpcLimitValue = limit.Value
		}
	case eglpc.DataUpdateFailsafeDurationMinimum:
		minDur, err := h.uceglpc.FailsafeDurationMinimum(entity)
		if err != nil {
			fmt.Println("Error getting FailsafeDurationMinimum:", err)
		} else {
			h.usecaseData.LpcFailsafeDur = minDur / time.Minute
		}
	case eglpc.DataUpdateFailsafeConsumptionActivePowerLimit:
		powerLimit, err := h.uceglpc.FailsafeConsumptionActivePowerLimit(entity)
		if err != nil {
			fmt.Println("Error getting FailsafeConsumptionActivePowerLimit:", err)
		} else {
			h.usecaseData.LpcFailsafePower = powerLimit
		}
	case eglpc.DataUpdateHeartbeat:
		h.usecaseData.LpcHeartbeatOk = h.uceglpc.IsHeartbeatWithinDuration(entity)
		h.usecaseData.LpcHeartbeatTimestamp = time.Now()
	}
	nominal, err := h.uceglpc.ConsumptionNominalMax(entity)
	if err == nil {
		h.usecaseData.LpcConsumptionLimitNominalMax = nominal
	}

	h.updateEntitiesFromDevice(device)
}

// HandleEgEvcc Energy Guard EVCC Handler
func (h *hems) HandleEgEvcc(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("EgEVCC Event: ", event)
	switch event {
	case cemevcc.UseCaseSupportUpdate:
		h.setUsecaseSupported("EVCC", true)
	case cemevcc.DataUpdateManufacturerData:
		manufacturer, err := h.uccemevcc.ManufacturerData(entity)
		if err != nil {
			fmt.Println("Error getting ManufacturerData:", err)
		} else {
			h.usecaseData.EvccManufacturerData = manufacturer
		}
	case cemevcc.DataUpdateChargeState:
		chargeState, err := h.uccemevcc.ChargeState(entity)
		if err != nil {
			fmt.Println("Error getting ChargeState:", err)
		} else {
			h.usecaseData.EvccChargeState = string(chargeState)
		}
	case cemevcc.DataUpdateAsymmetricChargingSupport:
		support, err := h.uccemevcc.AsymmetricChargingSupport(entity)
		if err != nil {
			fmt.Println("Error getting AsymmetricChargingSupport:", err)
		} else {
			h.usecaseData.EvccAsymmetricChargingSupport = support
		}
	case cemevcc.DataUpdateCommunicationStandard:
		standard, err := h.uccemevcc.CommunicationStandard(entity)
		if err != nil {
			fmt.Println("Error getting CommunicationStandard:", err)
		} else {
			h.usecaseData.EvccCommunicationStandard = string(standard)
		}
	case cemevcc.DataUpdateCurrentLimits:
		minimum, maximum, standby, err := h.uccemevcc.ChargingPowerLimits(entity)
		if err != nil {
			fmt.Println("Error getting ChargingPowerLimits:", err, minimum, maximum, standby)
		} else {
			h.usecaseData.EvccLimitMinimum = minimum
			h.usecaseData.EvccLimitMaximum = maximum
			h.usecaseData.EvccLimitStandby = standby
		}
	case cemevcc.DataUpdateIdentifications:
		identifications, err := h.uccemevcc.Identifications(entity)
		if err != nil {
			fmt.Println("Error getting Identifications:", err)
		} else {
			h.usecaseData.EvccIdentifications = identifications
		}

	case cemevcc.DataUpdateIsInSleepMode:
		sleepMode, err := h.uccemevcc.IsInSleepMode(entity)
		if err != nil {
			fmt.Println("Error getting IsInSleepMode:", err)
		} else {
			h.usecaseData.EvccSleepMode = sleepMode
		}
	case cemevcc.EvConnected:
		fmt.Println("EVCC Connected")
		h.usecaseData.EvccEvConnected = true
	case cemevcc.EvDisconnected:
		fmt.Println("EVCC Disconnected")
		h.usecaseData.EvccEvConnected = false
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
	switch event {
	case cemevsecc.UseCaseSupportUpdate:
		h.setUsecaseSupported("EVSECC", true)
	case cemevsecc.DataUpdateManufacturerData:
		manufacturer, err := h.uccemevsecc.ManufacturerData(entity)
		if err != nil {
			fmt.Println("Error getting ManufacturerData:", err)
		} else {
			h.usecaseData.EvseccManufacturerData = manufacturer
		}
	case cemevsecc.DataUpdateOperatingState:
		operatingState, errorMessage, err := h.uccemevsecc.OperatingState(entity)
		if err != nil {
			fmt.Println("Error getting OperatingState:", err)
		} else {
			h.usecaseData.EvseccOperatingState = string(operatingState)
			h.usecaseData.EvseccOperatingStateDescription = errorMessage
		}
	}
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
/*func (h *hems) HandleMaMpc(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
	fmt.Println("MaMpc Event: ", event)
	if event == mampc.UseCaseSupportUpdate {
		h.setUsecaseSupported("MPC", true)
	}
	h.updateEntitiesFromDevice(device)
}*/

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
	debug.PrintStack()
}

func (h *hems) Errorf(format string, args ...interface{}) {
	h.printFormat("ERRORF", format, args...)
	debug.PrintStack()
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
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("unknown command"))
			return
		}
	})

	// endpoint: return usecaseData (current values) in JSON-friendly units
	http.HandleFunc("/api/usecasedata", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(h.usecaseData); err != nil {
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

	addr := fmt.Sprintf("localhost:%d", webPort)
	h.Infof("Starting web interface on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		h.Errorf("web interface stopped: %v", err)
	}
}
