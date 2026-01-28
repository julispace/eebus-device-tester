# AGENTS.md - Device Tester (EEBUS)

## Project Overview

This application is an EEBUS device tester written in Go using the [eebus-go library](https://github.com/enbility/eebus-go). It simulates an Energy Management System (EMS) that connects to EEBUS-compatible devices (primarily EV chargers) for testing purposes.

The program consists of:
- **Backend**: Go application handling EEBUS protocol via SHIP/SPINE, usecase handlers, and REST API
- **Frontend**: Single-page HTML/CSS/JS web application displaying data and providing control interfaces

For implementation status and pending tasks, see [STATUS.md](STATUS.md). This should be read before every task and updated after finishing the task.

## Architecture

### Backend (main.go)

The backend is a single-file Go application (`main.go`) containing:

1. **EEBUS Service Setup**
   - Certificate handling and generation
   - Service configuration with device identity
   - Usecase initialization and registration

2. **Usecase Handlers**
   - Event handlers for each supported usecase
   - Data extraction and storage in `usecaseData` struct

3. **Write Functions**
   - Functions to send commands to remote devices
   - If the usecase has these write functions there should be write functions implemented

4. **Web Interface**
   - HTTP server on port 8080
   - WebSocket endpoint for real-time log streaming
   - REST API endpoints:
     - `POST /api/write` - Send commands to devices
     - `GET /api/usecasedata` - Get current usecase data
     - `GET /api/usecases` - Get supported usecases list
     - `GET /api/entities` - Get discovered entities
     - `GET /ws/logs` - WebSocket for logs and updates

5. **Data Structures**
   - `usecaseData` struct holds all usecase values
   - `hems` struct is the main application struct with service, usecases, and state

6. **Configuration System**
   - `config.json` file for enabling/disabling usecases
   - Loaded at startup; if not found, all usecases are enabled by default
   - Configuration served to frontend via `GET /api/config`

### Frontend (web/index.html)

The frontend should be split into a html file containing the HTML structure, CSS file containing the css styles, and a Js file containing the javascript logic. 
They are all included by the HTML file.

Key frontend features:
- Data panels for each usecase (LPC, LPP, EVSECC, EVCC, EVCEM, OPEV, OSCEV, EVSOC)
  - The data panels indicate whether a usecase is supported or not. This is indicated next to the Usecase name. if it is not supported all the fields are greyed out but still visible.
  - If the usecase on the backend has write operations it shall be possible to send values to the usecase on the backend and with that to the device. 
- SPINE message trace viewer showing the spine messages as they are parsed.
  - It shows the time when it was received, if it was a SEND or RECV message, the cmdclassifier, and the command type.
- Raw log viewer
- Entity tree display
  - This is the structure of the peer device
- Control inputs for write operations

## Specifications Reference

PDF specifications are located in `Spec/EEBUS/`:
- `EEBus_UC_TS_LimitationOfPowerConsumption_V1.0.0_public.pdf` - LPC
- `EEBus_UC_TS_LimitationOfPowerProduction_V1.0.0_public.pdf` - LPP
- `EEBus_UC_TS_CoordinatedEVCharging_V1.0.1.pdf` - CEVC
- `EEBus_UC_TS_EVCommissioningAndConfiguration_V1.0.1.pdf` - EVCC
- `EEBus_UC_TS_EVChargingElectricityMeasurement_V1.0.1.pdf` - EVCEM
- `EEBus_UC_TS_EVSECommissioningAndConfiguration_V1.0.1.pdf` - EVSECC
- `EEBus_UC_TS_EVChargingSummary_V1.0.1.pdf` - EVCS
- `EEBus_UC_TS_OverloadProtectionByEVChargingCurrentCurtailment_V1.0.1b.pdf` - OPEV
- `EEBus_UC_TS_MonitoringOfPowerConsumption_V1.0.0_public.pdf` - MPC

Additional specs in `Spec/EEBUS/Other Usecases/`:
- `EEBus_UC_TS_EVStateOfCharge_V1.0.0_RC1_public.pdf` - EVSOC
- `EEBus_UC_TS_OptimizationOfSelfConsumptionDuringEVCharging_V1.0.1b.pdf` - OSCEV

If these files are not available, use the eebus-go library as guideline. 

## Code Patterns

### Adding a New Usecase (Backend)

```go
// 1. Add import
import cemnewuc "github.com/enbility/eebus-go/usecases/cem/newuc"

// 2. Add to hems struct
type hems struct {
    // ...existing fields...
    uccemnewuc ucapi.CemNEWUCInterface
}

// 3. Add data fields to usecaseData
type usecaseData struct {
    // ...existing fields...
    NewucValue1 float64 `json:"newucValue1,omitempty"`
    NewucValue2 string  `json:"newucValue2,omitempty"`
}

// 4. Create handler
func (h *hems) HandleCemNewuc(ski string, device spineapi.DeviceRemoteInterface, entity spineapi.EntityRemoteInterface, event api.EventType) {
    fmt.Println("CemNewuc Event: ", event)
    switch event {
    case cemnewuc.UseCaseSupportUpdate:
        h.setUsecaseSupported("NEWUC", true)
    case cemnewuc.DataUpdateValue1:
        val, err := h.uccemnewuc.GetValue1(entity)
        if err == nil {
            h.usecaseData.NewucValue1 = val
        }
    }
    h.updateEntitiesFromDevice(device)
}

// 5. Initialize in run()
h.uccemnewuc = cemnewuc.NewNEWUC(localEntity, h.HandleCemNewuc)
h.myService.AddUseCase(h.uccemnewuc)
h.setUsecaseSupported("NEWUC", false)
```

### Adding a New Usecase (Frontend)

```html
<!-- 1. Add display section in Data and Control panel -->
<div class="usecase-label">NEWUC</div>
<div id="newucDisable" style="color:var(--muted);font-weight:600; display:flex;">not supported</div>
<div id="newucDisplay" style="display:none;flex-direction:column;gap:8px">
    <h5>Scenario 1 - Description</h5>
    <div class="data-container">
        <div class="data-label">Value 1</div>
        <div id="newucValue1" class="data-value">-</div>
    </div>
</div>
<div style="height:1px;background:grey;margin:6px 0"></div>
```

```javascript
// 2. Add display flag
let newucSupported = false;

// 3. Add display toggle function
function updateNEWUCDisplay(s) {
    newucSupported = s;
    const c = document.getElementById('newucDisplay'), d = document.getElementById('newucDisable');
    if (c && d) {
        if (s) { c.style.display = 'flex'; d.style.display = 'none'; }
        else { c.style.display = 'none'; d.style.display = 'flex'; }
    }
}

// 4. Register in setUsecase override
if (n === 'newuc') { updateNEWUCDisplay(supported); }

// 5. Initialize in DOMContentLoaded
updateNEWUCDisplay(false);

// 6. Update fetchUsecaseData
if (newucSupported) {
    setText('newucValue1', d.newucValue1);
}
```

## Configuration

The application supports runtime configuration via `config.json` to enable/disable usecases without recompiling.

### Config File Structure

```json
{
  "usecases": {
    "lpc": {
      "enabled": true,
      "description": "Limitation of Power Consumption (EG)"
    },
    "lpp": {
      "enabled": true,
      "description": "Limitation of Power Production (EG)"
    },
    "evcc": {
      "enabled": true,
      "description": "EV Commissioning and Configuration (CEM)"
    },
    "evcem": {
      "enabled": true,
      "description": "EV Charging Electricity Measurement (CEM)"
    },
    "evsecc": {
      "enabled": true,
      "description": "EVSE Commissioning and Configuration (CEM)"
    },
    "cevc": {
      "enabled": true,
      "description": "Coordinated EV Charging (CEM)"
    },
    "opev": {
      "enabled": true,
      "description": "Overload Protection by EV Charging Current Curtailment (CEM)"
    },
    "oscev": {
      "enabled": true,
      "description": "Optimization of Self-Consumption During EV Charging (CEM)"
    },
    "evsoc": {
      "enabled": true,
      "description": "EV State Of Charge (CEM)"
    },
    "mpc": {
      "enabled": true,
      "description": "Monitoring of Power Consumption (MA)"
    }
  }
}
```

### Configuration Behavior

- **File location**: `config.json` in the same directory as the executable
- **Missing file**: If `config.json` doesn't exist, all usecases are enabled by default
- **Invalid file**: If the file exists but is malformed, the application will fail to start with an error
- **Backend**: Disabled usecases are not initialized and don't consume resources
- **Frontend**: Disabled usecases are completely hidden from the UI
- **No restart needed**: Just edit `config.json` and restart the application

### Example: Disable MPC and CEVC

```json
{
  "usecases": {
    "lpc": {"enabled": true, "description": "Limitation of Power Consumption (EG)"},
    "lpp": {"enabled": true, "description": "Limitation of Power Production (EG)"},
    "evcc": {"enabled": true, "description": "EV Commissioning and Configuration (CEM)"},
    "evcem": {"enabled": true, "description": "EV Charging Electricity Measurement (CEM)"},
    "evsecc": {"enabled": true, "description": "EVSE Commissioning and Configuration (CEM)"},
    "cevc": {"enabled": false, "description": "Coordinated EV Charging (CEM)"},
    "opev": {"enabled": true, "description": "Overload Protection by EV Charging Current Curtailment (CEM)"},
    "oscev": {"enabled": true, "description": "Optimization of Self-Consumption During EV Charging (CEM)"},
    "evsoc": {"enabled": true, "description": "EV State Of Charge (CEM)"},
    "mpc": {"enabled": false, "description": "Monitoring of Power Consumption (MA)"}
  }
}
```

## Build and Run

```bash
# Build
go build

# Run (first time, generates certificates)
./device-tester <port>

# Run with remote SKI
./device-tester <port> <remoteSKI>

# Web UI
http://localhost:8080
```

After making changes to the go-code, run  "go build -a"

## Dependencies

Key dependencies from go.mod:
- `github.com/enbility/eebus-go` - EEBUS protocol implementation
- `github.com/enbility/ship-go` - SHIP protocol layer
- `github.com/enbility/spine-go` - SPINE protocol layer
- `github.com/gorilla/websocket` - WebSocket for log streaming
