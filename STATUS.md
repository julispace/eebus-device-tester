# STATUS.md - Device Tester Implementation Status

## EEBUS Usecases Implementation Status

### Relevant for EV Charger Testing (Power Consumer)

| Usecase | Full Name | Actor | Backend | Frontend | Write Controls |
|---------|-----------|-------|---------|----------|----------------|
| **LPC** | Limitation of Power Consumption | EG | Implemented | Implemented | Yes |
| **LPP** | Limitation of Power Production | EG | Implemented | Implemented | Yes |
| **EVCC** | EV Commissioning and Configuration | CEM | Implemented | Implemented | No |
| **EVCEM** | EV Charging Electricity Measurement | CEM | Implemented | Implemented | No |
| **EVSECC** | EVSE Commissioning and Configuration | CEM | Implemented | Implemented | No |
| **CEVC** | Coordinated EV Charging | CEM | Implemented | Implemented | No (planned) |
| **OPEV** | Overload Protection by EV Charging Current Curtailment | CEM | Implemented | Implemented | Yes |
| **OSCEV** | Optimization of Self-Consumption During EV Charging | CEM | Implemented | Implemented | Yes |
| **EVSOC** | EV State Of Charge | CEM | Implemented | Implemented | No |
| **MPC** | Monitoring of Power Consumption | MA | Implemented | Implemented | No |
| **MGCP** | Monitoring of Grid Connection Point | MA | Implemented | Implemented | No |
| **EVCS** | EV Charging Summary | CEM | Not available in eebus-go | Not implemented | No |

## Recently Completed Tasks

### SPINE Message Routing Fix
- **Issue**: SPINE trace messages were not being routed to the correct peer tabs
- **Root Cause**: The log format includes the SKI after the log level, but the frontend regex patterns didn't account for this
- **Log Format**: `2026-02-10 12:47:44 TRACE <SKI> Recv: <SKI> {"data":...}`
- **Frontend Fixes**:
  - Fixed `extractSKIFromMessage()` to match exactly 40 hex characters (SKI length)
  - Fixed trace message detection regex to expect SKI between TRACE and Recv/Send
  - Fixed `extractDirection()` to handle the same format
  - Now properly routes both `Recv` and `Send` message types

### Connect to Discovered Peers + Device Info Display
- **Backend Changes**:
  - Extended `peerData` struct with device info fields:
    - `deviceName`, `brand`, `model`, `deviceType`, `serial`, `identifier`
  - Extended `PeerInfo` struct with same fields for API responses
  - Updated `VisibleRemoteServicesUpdated()` to capture device info from mDNS discovery
  - Updated `broadcastPeerList()` to include device info in WebSocket messages
  - New API endpoint: `POST /api/connect` - accepts `{ski: "..."}` to register/connect to a peer
  - Updated `/api/peers` endpoint to return device information
- **Frontend Changes**:
  - New `connectToPeer(ski)` function to call the connect API
  - Peers List table now shows:
    - Full SKI (no longer truncated)
    - Device brand and model (e.g., "Tinkerforge WARP Energy Manager 2.0")
    - Additional details: device type, serial number, identifier
    - "Connect" button for disconnected peers (green button)
    - "Open" button for connected peers
  - Peer tab labels show device name if available, otherwise shortened SKI
  - Full SKI displayed in peer tab content area

### Multi-Peer Support (Major Refactoring) - Backend Complete
- **Architecture Changes**:
  - Removed `remoteSki` global variable - no longer needed for multi-peer support
  - Created new `peerData` struct containing:
    - `usecaseData` (per-peer data)
    - `entities []spineapi.EntityRemoteInterface`
    - `lastEntitiesJSON []byte`
    - `usecaseState map[string]bool`
    - `connected bool`
  - Updated `hems` struct:
    - Added `peers map[string]*peerData` (keyed by SKI)
    - Added `peersMu sync.Mutex`
    - Added `globalUseCaseState map[string]bool` for global usecase enablement tracking
    - Removed single `usecaseData`, `entities`, `lastEntitiesJSON`, `usecaseState` fields
  - Implemented peer management methods:
    - `getOrCreatePeer(ski string) *peerData` - gets existing or creates new peer
    - `getPeer(ski string) *peerData` - gets existing peer or nil
    - `removePeer(ski string)` - removes peer from map
    - `getAllPeers() map[string]*peerData` - returns copy of all peers
- **Event Handler Updates**:
  - All usecase handlers (HandleEgLPP, HandleEgLPC, HandleEgEvcc, HandleEgEvcem, HandleEgEvsecc, HandleEgCevc, HandleMaMpc, HandleMaMGCP, HandleCemOpev, HandleCemOscev, HandleCemEvsoc) updated to:
    - Get peer data using SKI parameter
    - Store data in peer's usecaseData instead of h.usecaseData
- **EEBUS Service Handler Updates**:
  - `ServicePairingDetailUpdate` - handles all peers, not just remoteSki
  - `AllowWaitingForTrust` - returns true for all SKIs initially
  - `VisibleRemoteServicesUpdated` - tracks discovered services, broadcasts peer list
  - `RemoteSKIConnected` - updates peer connection state to connected
  - `RemoteSKIDisconnected` - updates peer connection state to disconnected
- **Logging Improvements**:
  - Added `extractSKIFromMessage()` to parse SKI from trace entries
  - Log messages now include SKI for frontend routing
  - Each log line format: `<timestamp> <level> <ski> <message>`
- **New API Endpoints**:
  - `GET /api/peers` - list all discovered peers with connection state and usecase support
  - `GET /api/usecasedata?ski=<ski>` - get usecase data for specific peer (SKI parameter now required)
  - `GET /api/entities?ski=<ski>` - get entities for specific peer (SKI parameter now required)
- **WebSocket Messages**:
  - New message type `"peers"` broadcasts peer list updates
  - Entity updates include SKI for proper routing
  - Usecase support updates include SKI

### MGCP (Monitoring of Grid Connection Point) - Backend & Frontend Complete
- Backend implementation:
  - Added import for `github.com/enbility/eebus-go/usecases/ma/mgcp`
  - Added `ucmamgrp ucapi.MaMGCPInterface` to hems struct
  - Added data fields to usecaseData: `MgcPowerLimitationFactor`, `MgcPower`, `MgcEnergyFeedIn`, `MgcEnergyConsumed`, `MgcCurrentPerPhase`, `MgcVoltagePerPhase`, `MgcFrequency`
  - Created `HandleMaMGCP` event handler for all MGCP events: UseCaseSupportUpdate, DataUpdatePowerLimitationFactor, DataUpdatePower, DataUpdateEnergyFeedIn, DataUpdateEnergyConsumed, DataUpdateCurrentPerPhase, DataUpdateVoltagePerPhase, DataUpdateFrequency
  - Added MGCP initialization in `run()` function with config check
  - Added MGCP to default config in `getDefaultConfig()`
- Frontend implementation:
  - Added MGCP HTML section with all data fields displayed
  - Added `mgcpSupported` display flag
  - Added `mgcp` to usecase initialization list
  - Added MGCP data update logic in `fetchUsecaseData()`
- Configuration:
  - Added `mgcp` entry to config.json

### Frontend JavaScript Refactoring (Completed)
- Replaced individual `update<Usecase>Display()` functions with a generic `updateUsecaseDisplay()` function
- Updated HTML to use new element ID pattern: `<usecase>Badge` and `<usecase>Content`
- Added `cevcSupported` and `mpcSupported` display flags
- Added event handlers for LPP write buttons (limit, failsafe power, failsafe duration)
- Added event handlers for OSCEV write button (load control limit)
- Updated `fetchUsecaseData()` to populate CEVC and MPC fields

### CEVC (Coordinated EV Charging) - Backend & Frontend Complete
- Backend handlers implemented for all data events
- Frontend displays: charge strategy, energy demand (min/opt/max), duration until start/end, charge plan slots
- Write functions planned for future implementation

### MPC (Monitoring of Power Consumption) - Enabled
- Backend handler enabled (was previously commented out)
- Frontend displays: total power, power/current/voltage per phase, frequency, energy consumed/produced

### LPP (Limitation of Power Production) - Write Controls Added
- Backend write functions: `WriteLPPProductionLimit()`, `WriteLPPFailsafeDuration()`, `WriteLPPFailsafeValue()`
- Frontend write controls with input fields and send buttons

### OSCEV (Optimization of Self-Consumption) - Write Controls Added
- Backend write function: `WriteOSCEVLoadControlLimits()`
- Frontend write controls with input fields and send button

### OPEV (Overload Protection) - Write Controls Added (Latest)
- Backend write function: `WriteOPEVLoadControlLimits()`
- Frontend includes all 3 scenarios:
  - Scenario 1: Display and send load control limits
  - Scenario 2: Display current limits (min/max/default)
  - Scenario 3: Heartbeat info (CEM sends to EVSE)
- Frontend write controls with input fields and send button

### Logging Configuration - Moved to Config (Latest)
- **Backend**:
  - Moved `enableDebugLogging` and `enableTraceLogging` from global variables in main.go to config
  - Added `LoggingConfig` struct with `enableDebug` and `enableTrace` fields
  - Updated `Trace()`, `Tracef()`, `Debug()`, `Debugf()` methods to use config values
  - Both enabled by default when using default config
- **Config File** (`config.json`):
  - Added `logging` section with `enableDebug` and `enableTrace` options
  - Default values are `true` for both

### Configuration System - Implemented
- **Backend**: 
  - Added `Config` and `UsecaseConfig` structs
  - Added `loadConfig()` function to read `config.json`
  - Added `getDefaultConfig()` for fallback when file doesn't exist
  - Conditional usecase initialization based on config
  - New API endpoint: `GET /api/config` to serve config to frontend
- **Frontend**:
  - Added `loadConfig()` function to fetch config from backend
  - Added `hideUsecase()` function to completely hide disabled usecases from UI
  - Disabled usecases are hidden on page load
- **Config File** (`config.json`):
  - JSON format with enabled/disabled flags per usecase
  - If file doesn't exist, all usecases enabled by default
  - Located in same directory as executable
  - No restart needed - just edit and restart application
- **Documentation**: Updated AGENTS.md with configuration section

## Implementation Tasks

### High Priority

#### 1. Multi-Peer Frontend Support (Completed)
- Updated frontend with tabbed interface for multiple peers
- Peers List tab showing discovered peers with status, SKI, device info
- Individual peer tabs for each connected device
- WebSocket handling routes messages by SKI to appropriate peer
- Per-peer data storage: usecase data, entities, traces, logs
- Write operations include SKI parameter for correct routing
- Auto-refresh of peer data for active tab
- Tabs are closable with status indicators

#### 2. CEVC Write Functions (Pending)
- Add write functions for power limits and incentive tables
- Add frontend controls for sending power limits

### Low Priority

#### 2. EVCS (EV Charging Summary)
- Not yet available in eebus-go library. Mentioned in README as planned.

## eebus-go Library Usecases Reference

### Actor: CEM (Customer Energy Management)
- `cevc` - Coordinated EV Charging
- `evcc` - EV Commissioning and Configuration
- `evcem` - EV Charging Electricity Measurement
- `evsecc` - EVSE Commissioning and Configuration
- `evsoc` - EV State Of Charge
- `opev` - Overload Protection by EV Charging Current Curtailment
- `oscev` - Optimization of Self-Consumption During EV Charging
- `vabd` - Visualization of Aggregated Battery Data
- `vapd` - Visualization of Aggregated Photovoltaic Data

### Actor: CS (Controllable System)
- `lpc` - Limitation of Power Consumption
- `lpp` - Limitation of Power Production

### Actor: EG (Energy Guard)
- `lpc` - Limitation of Power Consumption
- `lpp` - Limitation of Power Production

### Actor: MA (Monitoring Appliance)
- `mpc` - Monitoring of Power Consumption
- `mgcp` - Monitoring of Grid Connection Point
