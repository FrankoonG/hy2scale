module github.com/FrankoonG/hy2scale

go 1.27.0

require (
	github.com/FrankoonG/hy2scale/core v0.0.0-00010101000000-000000000000
	github.com/apernet/hysteria/core/v2 v2.8.1
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/xen0bit/veepin v0.0.0-00010101000000-000000000000
	golang.org/x/crypto v0.57.0
	golang.org/x/net v0.60.0
	golang.org/x/sys v0.48.0
	golang.zx2c4.com/wireguard v0.0.0-20250521234502-f333402bd9cb
	gopkg.in/yaml.v3 v3.0.1
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c
)

require (
	github.com/apernet/quic-go v0.59.1-0.20260330051153-c402ee641eb6 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/stretchr/objx v0.5.2 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	golang.org/x/exp v0.0.0-20240506185415-9bf2ced13842 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/time v0.12.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
)

exclude golang.zx2c4.com/wireguard/tun/netstack v0.0.0-20220703234212-c31a7b1ab478

replace github.com/xen0bit/veepin => ./third_party/veepin

// The shared core (protocol, client, msess) is its own module so other
// projects can use it; this repository builds against the in-tree copy.
replace github.com/FrankoonG/hy2scale/core => ./core
