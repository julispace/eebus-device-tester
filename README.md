# Device Tester (EEBUS)

This app is a simple EEBUS device tester written in Go using the [eebus-go library](https://github.com/enbility/eebus-go). It can be used to test EEBUS implementations by simulating a device that connects to a EEBUS device:
- Starts a local EEBUS service and connects to a remote device using SHIP and SPINE
- Serves a web UI (showing logs, sent/received SPINE messages, usecases and discovered entities. Additionally it shows the values provided under these usecases.
- Provides basic REST APIs to trigger write operations (e.g. LPC limits). Not all write operations are Supported

## Supported Usecases
Several usecases are supported under various actors. The listed operations are supported:

- ### Actor: Energy Guard (EG
  - #### Limitation of Power Consumption (LPC)
    - Write Consumption Limit
    - Write Failsafe
  - #### Limitation of Power Production (LPP)
    - Not yet implemented in frontend
- ### Actor: Energy Manager (CEM)
  - #### Coordinated EV Charging (CEVC)
  - #### EV Comissioning  And Configuration (EVCC)
  - #### EV Charging Electricity Measrurement (EVCEM)
  - #### EVSE Commissioning And Configuration (EVSEC)
- ### Actor: Monitoring Appliance (MA)
  - #### Monitoring of PowerConsumption (MPC)
    - Not yet implemented

The usecase EV Charging Summary (EVCS) is planned to be supported once it becomes available.

## Quick build

```bash
# from project root
go build 
```

## Run

```bash
# required: server port
./device-tester <serverport>
# example
./device-tester 4815

# optional but recommended: provide remote SKI to connect to another device. Key and cert will be created
./device-tester 4815 <remoteski>
```

## Web UI

- Default UI: http://localhost:8080 
## Certificates

If `cert.pem`/`key.pem` are not present next to the executable, the program creates self-signed files on first run.


## Notes

Based on the [eebus-go controlbox example](https://github.com/enbility/eebus-go/tree/dev/examples/controlbox).

Additionally as I have never written GO before and have little experience in webdev, a sizeable portion has been written by coding agents. 
