package redis

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/coredns/coredns/request"
	"github.com/miekg/dns"

	redisCon "github.com/gomodule/redigo/redis"
)

const (
	DefaultTtl = 3600
)

type Redis struct {
	Pool           *redisCon.Pool
	address        string
	username       string
	password       string
	connectTimeout int
	readTimeout    int
	keyPrefix      string
	keySuffix      string
}

func New() *Redis {
	return &Redis{}
}

// SetAddress sets the address (host:port) to the redis backend
func (redis *Redis) SetAddress(a string) {
	redis.address = a
}

// SetUsername sets the username for the redis connection (optional)
func (redis Redis) SetUsername(u string) {
	//lint:ignore SA4005 incorrect warning
	redis.username = u
}

// SetPassword set the password for the redis connection (optional)
func (redis *Redis) SetPassword(p string) {
	redis.password = p
}

// SetKeyPrefix sets a prefix for all redis-keys (optional)
func (redis *Redis) SetKeyPrefix(p string) {
	redis.keyPrefix = p
}

// SetKeySuffix sets a suffix for all redis-keys (optional)
func (redis *Redis) SetKeySuffix(s string) {
	redis.keySuffix = s
}

// SetConnectTimeout sets a timeout in ms for the connection setup (optional)
func (redis *Redis) SetConnectTimeout(t int) {
	redis.connectTimeout = t
}

// SetReadTimeout sets a timeout in ms for redis read operations (optional)
func (redis *Redis) SetReadTimeout(t int) {
	redis.readTimeout = t
}

// Ping sends a "PING" command to the redis backend
// and returns (true, nil) if redis response
// is 'PONG'. Otherwise Ping return false and
// an error
func (redis *Redis) Ping() (bool, error) {
	conn := redis.Pool.Get()
	defer conn.Close()

	r, err := conn.Do("PING")
	s, err := redisCon.String(r, err)
	if err != nil {
		return false, err
	}
	if s != "PONG" {
		return false, fmt.Errorf("unexpected response, expected 'PONG', got: %s", s)
	}
	return true, nil
}

func (redis *Redis) ErrorResponse(state request.Request, zone string, rcode int, err error) (int, error) {
	m := new(dns.Msg)
	m.SetRcode(state.Req, rcode)
	m.Authoritative, m.RecursionAvailable, m.Compress = true, false, true

	state.SizeAndDo(m)
	_ = state.W.WriteMsg(m)
	// Return success as the rcode to signal we have written to the client.
	return dns.RcodeSuccess, err
}

// Connect establishes a connection to the redis-backend. The configuration must have
// been done before.
func (redis *Redis) Connect() error {
	redis.Pool = &redisCon.Pool{
		Dial: func() (redisCon.Conn, error) {
			var opts []redisCon.DialOption
			if redis.username != "" {
				opts = append(opts, redisCon.DialUsername(redis.username))
			}
			if redis.password != "" {
				opts = append(opts, redisCon.DialPassword(redis.password))
			}
			if redis.connectTimeout != 0 {
				opts = append(opts, redisCon.DialConnectTimeout(time.Duration(redis.connectTimeout)*time.Millisecond))
			}
			if redis.readTimeout != 0 {
				opts = append(opts, redisCon.DialReadTimeout(time.Duration(redis.readTimeout)*time.Millisecond))
			}

			return redisCon.Dial("tcp", redis.address, opts...)
		},
	}
	c := redis.Pool.Get()
	defer c.Close()

	if c.Err() != nil {
		return c.Err()
	}

	res, err := c.Do("PING")
	pong, err := redisCon.String(res, err)
	if err != nil {
		return err
	}
	if pong != "PONG" {
		return fmt.Errorf("unexpexted result, 'PONG' expected: %s", pong)
	}
	return nil
}

// Produce a RRSet with at least one record, from potentially multiple IPv4 addresses
func (redis *Redis) parseA(ips []string, recordName, zoneName string, header dns.RR_Header) []dns.RR {
	var answers []dns.RR
	for _, ip := range ips {
		r := new(dns.A)
		header.Name = recordName
		header.Rrtype = dns.TypeA
		r.Hdr = header
		r.A = net.ParseIP(ip)
		answers = append(answers, r)
	}
	return answers
}

// Produce a RRSet with at least one record from each configured
// nameserver, and additional records produced from resolving these
// these nameserver to their IPv4 addresses.
func (redis *Redis) parseNS(hosts []string, zoneName string, header dns.RR_Header, conn redisCon.Conn) (answers, extras []dns.RR, err error) {
	for _, host := range hosts {
		r := new(dns.NS)
		header.Name = zoneName
		header.Rrtype = dns.TypeNS
		r.Hdr = header
		if !dns.IsFqdn(host) {
			err = fmt.Errorf("host %s musr be fully qualified", host)
			return
		}
		r.Ns = host
		answers = append(answers, r)
		var additional []dns.RR
		additional, err = redis.getAdditionalRecords(host, zoneName, conn)
		if err != nil {
			return
		}
		extras = append(extras, additional...)
	}
	return
}

// Produce a RRSet with one SOA record, and optional additional
// records produced from resolving the NameServer.
func (redis *Redis) parseSOA(fields []string, zoneName string, header dns.RR_Header, conn redisCon.Conn) (answers, extras []dns.RR, err error) {
	r := new(dns.SOA)
	header.Name = zoneName
	header.Rrtype = dns.TypeSOA
	r.Hdr = header
	r.Ns, r.Mbox = fields[0], fields[1]
	if !dns.IsFqdn(r.Mbox) {
		r.Mbox = fmt.Sprintf("%s.%s", r.Mbox, zoneName)
	}

	var x int
	if x, err = strconv.Atoi(fields[2]); err != nil {
		return
	}
	r.Serial = uint32(x)

	if x, err = strconv.Atoi(fields[3]); err != nil {
		return
	}
	r.Refresh = uint32(x)

	if x, err = strconv.Atoi(fields[4]); err != nil {
		return
	}
	r.Retry = uint32(x)

	if x, err = strconv.Atoi(fields[5]); err != nil {
		return
	}
	r.Expire = uint32(x)

	if x, err = strconv.Atoi(fields[6]); err != nil {
		return
	}
	r.Minttl = uint32(x)

	// Append any additional records which might be produced
	// by resolving the SOA's record nameserver.
	additional, err := redis.getAdditionalRecords(fields[0], zoneName, conn)
	if err != nil {
		return
	}

	extras = append(extras, additional...)
	answers = append(answers, r)
	return
}

func (redis *Redis) parseRecordValuesFromString(recordType, recordName, zoneName, rData string, conn redisCon.Conn) (answers, extras []dns.RR, err error) {
	// array of string fiels as parsed from Redis
	// e.g. ['200', 'IN', 'A', '1.2.3.4', ...]
	fields := strings.Fields(rData)
	if len(fields) < 4 {
		err = fmt.Errorf("error parsing RData for %s/%s.%s: invalid number of elements", recordType, recordName, zoneName)
		return
	}
	if recordType != fields[2] {
		err = fmt.Errorf("error: mismatch record type for %s.%s: %s != %s", recordName, zoneName, recordType, fields[2])
		return
	}
	ttl, err := strconv.Atoi(fields[0])
	if err != nil {
		err = fmt.Errorf("error parsing TTL literal '%s': %s", fields[0], err)
		return
	}

	// Common attributes in all DNS records
	header := dns.RR_Header{
		Class: dns.ClassINET,
		Ttl:   uint32(ttl),
	}

	switch recordType {
	case "A":
		answers = redis.parseA(fields[3:], recordName, zoneName, header)
	case "NS":
		answers, extras, err = redis.parseNS(fields[3:], zoneName, header, conn)
	case "SOA":
		answers, extras, err = redis.parseSOA(fields[3:], zoneName, header, conn)
	default:
		err = fmt.Errorf("unknown record type %s", recordType)
	}
	return
}

func (redis *Redis) getAdditionalRecords(recordName, zoneName string, conn redisCon.Conn) (answers []dns.RR, err error) {
	answers, extras, err := redis.LoadZoneRecords("A", recordName, zoneName, conn)
	if err == nil {
		if len(extras) > 0 {
			err = fmt.Errorf("unexpected additional resources for A/%s.%s", recordName, zoneName)
			return
		}
	}
	return
}

func (redis *Redis) LoadZoneRecords(recordType, recordName, zoneName string, conn redisCon.Conn) (answers, extras []dns.RR, err error) {
	var (
		keyName      string
		ttlKeyName   string
		rData        string // RR data
		remainingTtl int    // remaining TTL (from Redis)
	)

	keyName = fmt.Sprintf("%s/%s", recordType, recordName)
	ttlKeyName = fmt.Sprintf("%s:ttl", keyName)

	err = conn.Send("MULTI")
	if err != nil {
		return
	}
	err = conn.Send("GET", redis.Key(keyName))
	if err != nil {
		return
	}
	err = conn.Send("TTL", redis.Key(ttlKeyName))
	if err != nil {
		return
	}
	values, err := redisCon.Values(conn.Do("EXEC"))
	if err != nil {
		return
	}
	_, err = redisCon.Scan(values, &rData, &remainingTtl)
	if err != nil {
		return
	}
	if rData == "" {
		err = fmt.Errorf("no RData for %s", keyName)
		return
	}
	answers, extras, err = redis.parseRecordValuesFromString(recordType, recordName, zoneName, rData, conn)
	if err != nil {
		return
	}

	// Support for monotonically decreasing TTLs
	if remainingTtl == -2 {
		// TTL shall be the same for all records in a RRset, so we
		// take the first one
		newTtl := uint32(answers[0].Header().Ttl)
		// If no Redis TTL key for the given DNS RRSet exists yet,
		// insert a special TTL key in Redis for it
		_, err = conn.Do("SET", redis.Key(ttlKeyName), newTtl, "EX", newTtl)
		if err != nil {
			err = fmt.Errorf("error configuring TTL for %s: %s", keyName, err)
			return
		}
	} else {
		// If a Redis TTL key for the given RRSet exists, yield
		// the remaining TTL for it
		for _, answer := range answers {
			answer.Header().Ttl = uint32(remainingTtl)
		}
	}
	return
}

// LoadAllZoneNames returns all zone names saved in the backend
func (redis *Redis) LoadAllZoneNames() ([]string, error) {
	conn := redis.Pool.Get()
	defer conn.Close()

	reply, err := conn.Do("KEYS", redis.keyPrefix+"*"+redis.keySuffix)
	zones, err := redisCon.Strings(reply, err)
	if err != nil {
		return nil, err
	}
	for i := range zones {
		zones[i] = strings.TrimPrefix(zones[i], redis.keyPrefix)
		zones[i] = strings.TrimSuffix(zones[i], redis.keySuffix)
	}
	return zones, nil
}

// Key returns the given key with prefix
func (redis *Redis) Key(zoneName string) string {
	return redis.keyPrefix + zoneName
}

// TtlKey returns the given key used to keep track of decreasing TTLs
func (redis *Redis) TtlKey(recordType, recordName, zoneName string) string {
	return redis.keyPrefix + zoneName + ":ttl:" + recordName + "/" + recordType
}
