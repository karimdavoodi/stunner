package cmd

import (
	"errors"
	"fmt"
	"math/rand"
	"net/netip"
	"strings"
	"time"

	"github.com/firefart/stunner/internal"
	"github.com/firefart/stunner/internal/helper"
	"github.com/sirupsen/logrus"
)

type UDPScannerOpts struct {
	TurnServer      string
	Protocol        string
	Username        string
	Password        string
	UseTLS          bool
	TlsVerify       bool
	Timeout         time.Duration
	Log             *logrus.Logger
	CommunityString string
	DomainName      string
	IPs             []string
}

func (opts UDPScannerOpts) Validate() error {
	if opts.TurnServer == "" {
		return fmt.Errorf("need a valid turnserver")
	}
	if !strings.Contains(opts.TurnServer, ":") {
		return fmt.Errorf("turnserver needs a port")
	}
	if opts.Protocol != "tcp" && opts.Protocol != "udp" {
		return fmt.Errorf("protocol needs to be either tcp or udp")
	}
	if opts.Username == "" {
		return fmt.Errorf("please supply a username")
	}
	if opts.Password == "" {
		return fmt.Errorf("please supply a password")
	}
	if opts.Log == nil {
		return fmt.Errorf("please supply a valid logger")
	}
	if opts.CommunityString == "" {
		return fmt.Errorf("please supply a valid community string")
	}
	if opts.DomainName == "" {
		return fmt.Errorf("please supply a valid domain name")
	}
	// no need to check IPs, it can be nil

	return nil
}

func UDPScanner(opts UDPScannerOpts) error {
	if err := opts.Validate(); err != nil {
		return err
	}

	ipInput := opts.IPs
	if len(ipInput) == 0 {
		ipInput = helper.PrivateRanges
	}

	ipChan := helper.IPIterator(ipInput)

	for ip := range ipChan {
		if ip.Error != nil {
			opts.Log.Error(ip.Error)
			continue
		}
		opts.Log.Debugf("Scanning %s", ip.IP.String())
		if err := snmpScan(opts, ip.IP, 161, opts.CommunityString); err != nil {
			opts.Log.Errorf("error on running SNMP Scan for ip %s: %v", ip.IP.String(), err)
		}
		if err := dnsScan(opts, ip.IP, 53, opts.DomainName); err != nil {
			opts.Log.Errorf("error on running DNS Scan for ip %s: %v", ip.IP.String(), err)
		}
	}

	return nil
}

func snmpScan(opts UDPScannerOpts, ip netip.Addr, port uint16, community string) error {
	remote, realm, nonce, err := internal.SetupTurnConnection(opts.Log, opts.Protocol, opts.TurnServer, opts.UseTLS, opts.TlsVerify, opts.Timeout, ip, port, opts.Username, opts.Password)
	if err != nil {
		// ignore timeouts
		if errors.Is(err, helper.ErrTimeout) {
			return nil
		}
		return err
	}
	defer remote.Close()

	channelNumber := helper.RandomChannelNumber()
	channelBindRequest, err := internal.ChannelBindRequest(opts.Username, opts.Password, nonce, realm, ip, port, channelNumber)
	if err != nil {
		return fmt.Errorf("error on generating ChannelBindRequest: %w", err)
	}

	channelBindResponse, err := channelBindRequest.SendAndReceive(opts.Log, remote, opts.Timeout)
	if err != nil {
		return fmt.Errorf("error on sending ChannelBindRequest: %w", err)
	}

	if channelBindResponse.Header.MessageType.Class == internal.MsgTypeClassError {
		return fmt.Errorf("error on ChannelBind: %s", channelBindResponse.GetErrorString())
	}

	var snmp []byte
	var inner []byte
	// junk before version
	inner = append(inner, 0x02)
	inner = append(inner, 0x01)
	// version 1 == v2c
	inner = append(inner, 1)
	// 4 - some random stuff
	inner = append(inner, 0x04)
	// length of community string
	inner = append(inner, uint8(len(community)))
	// community string
	inner = append(inner, []byte(community)...)
	// get-next 1.3.6.1.2.1
	inner = append(inner, []byte{0xa1, 0x19, 0x02, 0x04}...)
	// request ID
	inner = append(inner, helper.PutUint32(rand.Uint32())...)
	// rest
	inner = append(inner, 0x02, 0x01, 0x00, 0x02, 0x01, 0x00, 0x30, 0x0b, 0x30, 0x09, 0x06, 0x05, 0x2b, 0x06, 0x01, 0x02, 0x01, 0x05, 0x00)

	// Sequence
	snmp = append(snmp, 0x30)
	// Overall Length
	snmp = append(snmp, uint8(len(inner)))
	snmp = append(snmp, inner...)

	snmpLen := len(snmp)

	var buf []byte
	buf = append(buf, channelNumber...)
	buf = append(buf, helper.PutUint16(uint16(snmpLen))...)
	buf = append(buf, snmp...)

	err = helper.ConnectionWrite(remote, buf, opts.Timeout)
	if err != nil {
		return fmt.Errorf("error on sending SNMP request: %w", err)
	}

	resp, err := helper.ConnectionRead(remote, opts.Timeout)
	if err != nil {
		// ignore timeouts
		if errors.Is(err, helper.ErrTimeout) {
			return nil
		}
		return fmt.Errorf("error on reading SNMP response: %w", err)
	}

	channel, data, err := internal.ExtractChannelData(resp)
	if err != nil {
		return err
	}

	opts.Log.Infof("received %d bytes on channel %02x for ip %s", len(data), channel, ip.String())
	opts.Log.Infof("UDP Response: %s", string(resp))

	return nil
}

func dnsScan(opts UDPScannerOpts, ip netip.Addr, port uint16, dnsName string) error {
	remote, realm, nonce, err := internal.SetupTurnConnection(opts.Log, opts.Protocol, opts.TurnServer, opts.UseTLS, opts.TlsVerify, opts.Timeout, ip, port, opts.Username, opts.Password)
	if err != nil {
		// ignore timeouts
		if errors.Is(err, helper.ErrTimeout) {
			return nil
		}
		return err
	}
	defer remote.Close()

	channelNumber := helper.RandomChannelNumber()
	channelBindRequest, err := internal.ChannelBindRequest(opts.Username, opts.Password, nonce, realm, ip, port, channelNumber)
	if err != nil {
		return fmt.Errorf("error on generating ChannelBindRequest: %w", err)
	}

	channelBindResponse, err := channelBindRequest.SendAndReceive(opts.Log, remote, opts.Timeout)
	if err != nil {
		return fmt.Errorf("error on sending ChannelBindRequest: %w", err)
	}

	if channelBindResponse.Header.MessageType.Class == internal.MsgTypeClassError {
		return fmt.Errorf("error on ChannelBind: %s", channelBindResponse.GetErrorString())
	}

	var dns []byte

	// transactionID
	dns = append(dns, helper.PutUint16(uint16(rand.Uint32()))...)
	// FLAGS: standard query
	dns = append(dns, []byte{0x01, 0x00}...)
	// Questions: 1
	dns = append(dns, helper.PutUint16(1)...)
	// Answer RRs: 0
	dns = append(dns, helper.PutUint16(0)...)
	// Authority RRs: 0
	dns = append(dns, helper.PutUint16(0)...)
	// Additional RRs: 0
	dns = append(dns, helper.PutUint16(0)...)

	// Query: LEN, DOMAIN (null byte terminated), 0x0001, 0x0001
	domainParts := strings.Split(dnsName, ".")
	var domainBuf []byte
	for _, x := range domainParts {
		domainBuf = append(domainBuf, uint8(len(x)))
		domainBuf = append(domainBuf, []byte(x)...)
	}
	// terminate with a null byte
	domainBuf = append(domainBuf, 0x00)
	// Type A
	domainBuf = append(domainBuf, helper.PutUint16(1)...)
	// Class: IN
	domainBuf = append(domainBuf, helper.PutUint16(1)...)

	dns = append(dns, domainBuf...)

	dnsLen := len(dns)

	var buf []byte
	buf = append(buf, channelNumber...)
	buf = append(buf, helper.PutUint16(uint16(dnsLen))...)
	buf = append(buf, dns...)

	err = helper.ConnectionWrite(remote, buf, opts.Timeout)
	if err != nil {
		return fmt.Errorf("error on sending DNS request: %w", err)
	}

	resp, err := helper.ConnectionRead(remote, opts.Timeout)
	if err != nil {
		// ignore timeouts
		if errors.Is(err, helper.ErrTimeout) {
			return nil
		}
		return fmt.Errorf("error on reading DNS response: %w", err)
	}

	channel, data, err := internal.ExtractChannelData(resp)
	if err != nil {
		return err
	}

	opts.Log.Infof("received %d bytes on channel %02x for ip %s", len(data), channel, ip.String())
	opts.Log.Infof("UDP Response: %s", string(resp))

	return nil
}
