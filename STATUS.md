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
| **OPEV** | Overload Protection by EV Charging Current Curtailment | CEM | Implemented | Implemented | No |
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

## Implementation Tasks

### High Priority

#### 1. CEVC Write Functions (Pending)
- Add write functions for power limits and incentive tables
- Add frontend controls for sending power limits

### Medium Priority

#### 2. OPEV Write Functions
- Add write functions similar to OSCEV for overload protection limits

### Low Priority

#### 3. EVCS (EV Charging Summary)
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
