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
| **EVCS** | EV Charging Summary | CEM | Not available in eebus-go | Not implemented | No |

## Recently Completed Tasks

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

### Configuration System - Implemented (Latest)
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

#### 1. CEVC Write Functions (Pending)
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
