package l2tp

// Client option metadata for the Dial surface.

import "github.com/xen0bit/veepin/client"

func init() {
	client.RegisterClientOpts("l2tp", []client.OptSpec{
		{Key: OptServer, Kind: client.OptStr, Required: true, Help: "L2TP/IPsec server host or IP"},
		{Key: OptPort, Kind: client.OptInt, Default: "500", Help: "server IKE/ESP port (default 500)"},
		{Key: OptPSK, Kind: client.OptStr, Secret: true, Required: true, Help: "IPsec pre-shared key"},
		{Key: OptUser, Kind: client.OptStr, Required: true, Help: "MS-CHAPv2 username"},
		{Key: OptPassword, Flag: "pass", Kind: client.OptStr, Secret: true, Required: true, Help: "MS-CHAPv2 password"},
		{Key: OptDNS, Kind: client.OptCommaList, Help: "comma-separated DNS servers (fallback if PPP assigns none)"},
		client.TUNOpt(OptTUNName),
	})
}
