module demo_api

go 1.25.0

toolchain go1.26.10

require (
	fix_bridge v0.0.0
	github.com/gorilla/websocket v1.5.3
	go_gateway v0.0.0
	google.golang.org/protobuf v1.36.12
)

replace go_gateway => ../go_gateway
replace fix_bridge => ../fix_bridge
