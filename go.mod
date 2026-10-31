module github.com/FrankoonG/hy2scale

go 1.27.0

require (
	github.com/FrankoonG/hy2scale/core v0.0.0-00010101000000-000000000000
	github.com/apernet/hysteria/core/v2 v2.13.0
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/xen0bit/veepin v0.0.0-00010101000000-000000000000
	golang.org/x/crypto v0.57.0
	golang.org/x/sys v0.48.0
	golang.zx2c4.com/wireguard v0.0.0-20250521234502-f333402bd9cb
	gopkg.in/yaml.v3 v3.0.1
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c
)

require (
	github.com/andybalholm/brotli v1.1.0 // indirect
	github.com/apernet/quic-go v0.63.1-0.20261004180939-a10df75c260c // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/refraction-networking/utls v1.8.2 // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/exp v0.0.0-20240506185415-9bf2ced13842 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
)

exclude golang.zx2c4.com/wireguard/tun/netstack v0.0.0-20220703234212-c31a7b1ab478

replace github.com/xen0bit/veepin => ./third_party/veepin

// The shared core (protocol, client, msess) is its own module so other
// projects can use it; this repository builds against the in-tree copy.
replace github.com/FrankoonG/hy2scale/core => ./core
