package geoip

import (
	"net/netip"
	"strconv"
	"strings"
)

// Result is what is known about one address (POST /api/geoip/lookup).
// Unknown fields are omitted; an empty Result means "not found".
type Result struct {
	Private   bool     `json:"private,omitempty"`
	Country   string   `json:"country,omitempty"`   // ISO 3166-1 alpha-2
	Continent string   `json:"continent,omitempty"` // two-letter code
	Region    string   `json:"region,omitempty"`    // most specific subdivision
	City      string   `json:"city,omitempty"`
	Postal    string   `json:"postal,omitempty"`
	Lat       *float64 `json:"lat,omitempty"`
	Lon       *float64 `json:"lon,omitempty"`
	TZ        string   `json:"tz,omitempty"`
	ASN       uint     `json:"asn,omitempty"`
	Org       string   `json:"org,omitempty"`
	NamesLang string   `json:"names_lang,omitempty"` // language of region / city
	Error     string   `json:"error,omitempty"`
}

// cgnat is the shared address space of RFC 6598.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// IsPrivate says whether ip is never looked up: private, loopback,
// link-local, CGNAT, unspecified or multicast.
func IsPrivate(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip)
}

// DBLang is the database language used for an API language: zh → zh-CN,
// anything else as given ("" = en). Names missing in it fall back to en.
func DBLang(lang string) string {
	switch l := strings.TrimSpace(lang); strings.ToLower(l) {
	case "", "en":
		return "en"
	case "zh", "zh-cn", "zh-hans":
		return "zh-CN"
	case "ko", "ko-kr":
		return "ko"
	default:
		return l
	}
}

// LookupString is Lookup of a textual address ("[v6]" accepted).
func (s *Store) LookupString(ip, lang string) Result {
	a, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(ip), "[]"))
	if err != nil {
		return Result{Error: "invalid ip"}
	}
	return s.Lookup(a, lang)
}

type cacheKey struct {
	ip   netip.Addr
	lang string
}

// Lookup answers what the loaded databases know about ip, names in lang
// (an API language: en, zh, ko …).
func (s *Store) Lookup(ip netip.Addr, lang string) Result {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() {
		return Result{Error: "invalid ip"}
	}
	if IsPrivate(ip) {
		return Result{Private: true}
	}
	want := DBLang(lang)
	k := cacheKey{ip, want}
	if r, ok := s.cache.get(k); ok {
		return r
	}
	s.mu.RLock()
	gen := s.cache.generation()
	var out Result
	for _, kind := range Kinds {
		d := s.dbs[kind]
		if d == nil {
			continue
		}
		if ip.Is6() && d.info.IPVersion == 4 {
			continue
		}
		res := d.r.Lookup(ip)
		if !res.Found() {
			continue
		}
		var m map[string]any
		if err := res.Decode(&m); err != nil {
			continue
		}
		r := decodeRecord(m, langChain(want, d.info.Languages))
		if kind == KindASN {
			// only the AS: the country in an ASN record is where the AS is
			// registered, not where the address is
			r = Result{ASN: r.ASN, Org: r.Org}
		}
		merge(&out, r)
	}
	s.mu.RUnlock()
	s.cache.put(k, out, gen)
	return out
}

// langChain: the languages names are taken from, best first.
func langChain(want string, have []string) []string {
	if want == "en" {
		return []string{"en"}
	}
	if len(have) > 0 {
		ok := false
		for _, l := range have {
			if l == want {
				ok = true
			}
		}
		if !ok {
			return []string{"en"}
		}
	}
	return []string{want, "en"}
}

// merge fills what out does not know yet from r. Place names come
// together from the first database that has any; so do ASN and org.
func merge(out *Result, r Result) {
	if out.Country == "" {
		out.Country = r.Country
	}
	if out.Continent == "" {
		out.Continent = r.Continent
	}
	if out.City == "" && out.Region == "" && (r.City != "" || r.Region != "") {
		out.City, out.Region, out.NamesLang = r.City, r.Region, r.NamesLang
	}
	if out.Postal == "" {
		out.Postal = r.Postal
	}
	if out.Lat == nil && r.Lat != nil {
		out.Lat, out.Lon = r.Lat, r.Lon
	}
	if out.TZ == "" {
		out.TZ = r.TZ
	}
	if out.ASN == 0 && out.Org == "" {
		out.ASN, out.Org = r.ASN, r.Org
	}
}

// decodeRecord reads one record: GeoIP2 / GeoLite2 field names (also used
// by DB-IP), or the flat names of other mmdb publishers (ipinfo lite,
// ip-location-db). Names are taken in the first of langs they exist in.
func decodeRecord(m map[string]any, langs []string) Result {
	var r Result
	// country
	if c, ok := m["country"].(map[string]any); ok {
		r.Country = str(c["iso_code"])
	}
	if r.Country == "" {
		if c, ok := m["registered_country"].(map[string]any); ok {
			r.Country = str(c["iso_code"])
		}
	}
	if r.Country == "" {
		r.Country = str(m["country_code"])
		if r.Country == "" {
			if c := str(m["country"]); len(c) == 2 {
				r.Country = c
			}
		}
	}
	r.Country = strings.ToUpper(r.Country)
	// continent
	if c, ok := m["continent"].(map[string]any); ok {
		r.Continent = str(c["code"])
	} else {
		r.Continent = str(m["continent_code"])
		if c := str(m["continent"]); r.Continent == "" && len(c) == 2 {
			r.Continent = c
		}
	}
	r.Continent = strings.ToUpper(r.Continent)

	// names: city and the most specific subdivision
	nameLang := ""
	pick := func(v any) string {
		names, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		nm, _ := names["names"].(map[string]any)
		for _, l := range langs {
			if s := str(nm[l]); s != "" {
				if nameLang == "" {
					nameLang = l
				}
				return s
			}
		}
		return ""
	}
	if c, ok := m["city"].(map[string]any); ok {
		r.City = pick(c)
	} else if c := str(m["city"]); c != "" {
		r.City, nameLang = c, "en"
	}
	if subs, ok := m["subdivisions"].([]any); ok && len(subs) > 0 {
		for i := len(subs) - 1; i >= 0 && r.Region == ""; i-- {
			r.Region = pick(subs[i])
		}
	} else {
		for _, k := range []string{"state2", "region", "state1", "subdivision"} {
			if v := str(m[k]); v != "" {
				r.Region = v
				if nameLang == "" {
					nameLang = "en"
				}
				break
			}
		}
	}
	if r.City != "" || r.Region != "" {
		r.NamesLang = nameLang
	}
	// postal, location
	if p, ok := m["postal"].(map[string]any); ok {
		r.Postal = str(p["code"])
	} else {
		r.Postal = firstStr(m, "postcode", "postal_code", "zip")
	}
	if l, ok := m["location"].(map[string]any); ok {
		r.Lat, r.Lon = num(l["latitude"]), num(l["longitude"])
		r.TZ = str(l["time_zone"])
	} else {
		r.Lat, r.Lon = num(m["latitude"]), num(m["longitude"])
		r.TZ = firstStr(m, "timezone", "time_zone")
	}
	if r.Lat == nil || r.Lon == nil {
		r.Lat, r.Lon = nil, nil
	}
	// ASN
	if n := num(m["autonomous_system_number"]); n != nil && *n > 0 {
		r.ASN = uint(*n)
	} else if a := str(m["asn"]); a != "" {
		if v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToUpper(a), "AS"), 10, 32); err == nil {
			r.ASN = uint(v)
		}
	} else if n := num(m["asn"]); n != nil && *n > 0 {
		r.ASN = uint(*n)
	}
	r.Org = firstStr(m, "autonomous_system_organization", "as_name", "as_org", "organization", "org")
	if r.Org == "" && r.ASN != 0 {
		r.Org = str(m["name"]) // IPLocate: org, else the AS name
	}
	if r.ASN == 0 && r.Org == "" {
		if t, ok := m["traits"].(map[string]any); ok {
			if n := num(t["autonomous_system_number"]); n != nil && *n > 0 {
				r.ASN = uint(*n)
			}
			r.Org = str(t["autonomous_system_organization"])
		}
	}
	return r
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(m[k]); s != "" {
			return s
		}
	}
	return ""
}

func num(v any) *float64 {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case float32:
		f = float64(x)
	case uint64:
		f = float64(x)
	case uint32:
		f = float64(x)
	case uint16:
		f = float64(x)
	case int:
		f = float64(x)
	case int32:
		f = float64(x)
	case int64:
		f = float64(x)
	case uint:
		f = float64(x)
	default:
		return nil
	}
	return &f
}
