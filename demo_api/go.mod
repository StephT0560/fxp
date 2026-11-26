module demo_api

go 1.25.0

toolchain go1.26.11

require (
	fix_bridge v0.0.0
	github.com/gorilla/websocket v1.5.4-0.20240701034025-d67f41855da4
	go_gateway v0.0.0
	google.golang.org/protobuf v1.36.12
)

require golang.org/x/net v0.26.0 // indirect

replace go_gateway => ../go_gateway

replace fix_bridge => ../fix_bridge
