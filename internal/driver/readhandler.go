// -*- Mode: Go; indent-tabs-mode: t -*-
//
// Copyright (C) 2018 Canonical Ltd
// Copyright (C) 2018 IOTech Ltd
// Copyright (C) 2021 Schneider Electric
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	sdkModel "github.com/edgexfoundry/device-sdk-go/v2/pkg/models"
	"github.com/edgexfoundry/go-mod-core-contracts/v2/models"
	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
	"log"
	"math/big"
	"net"
	"net/url"
	"os"
	"time"
)

var (
	certfile = ""
	keyfile  = ""
)

// HandleReadCommands triggers a protocol Read operation for the specified device.
func (d *Driver) HandleReadCommands(deviceName string, protocols map[string]models.ProtocolProperties,
	reqs []sdkModel.CommandRequest) ([]*sdkModel.CommandValue, error) {

	d.Logger.Debugf("Driver.HandleReadCommands: protocols: %v resource: %v attributes: %v", protocols, reqs[0].DeviceResourceName, reqs[0].Attributes)

	endpoints, err := opcua.GetEndpoints("opc.tcp://10.249.20.40:4840")
	if err != nil {
		log.Print("OPC GetEndpoints: %w", err)
	}

	policy := ua.SecurityPolicyURIBasic128Rsa15  // Replace this with a constant of your security policy
	mode := ua.MessageSecurityModeSignAndEncrypt // Replace this with a constant of your security mode
	ep := opcua.SelectEndpoint(endpoints, policy, mode)
	c, err := generateCert() // This is where you generate the certificate
	if err != nil {
		log.Print("generateCert: %w", err)
	}

	pk, ok := c.PrivateKey.(*rsa.PrivateKey) // This is where you set the private key
	if !ok {
		log.Print("invalid private key")
	}

	cert := c.Certificate[0]

	opts := []opcua.Option{
		opcua.SecurityPolicy(policy),
		opcua.SecurityMode(mode),
		opcua.PrivateKey(pk),
		opcua.Certificate(cert),                // Set the certificate for the OPC UA Client
		opcua.AuthUsername("bTrace", "bTrace"), // Use this if you are using username and password
		opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeUserName),
		opcua.SessionTimeout(30 * time.Minute),
	}

	ctx := context.Background()
	client := opcua.NewClient("opc.tcp://10.249.20.40:4840", opts...)
	if err := client.Connect(ctx); err != nil {
		log.Printf("Driver.HandleReadCommands: Failed to connect OPCUA client, %s", err)
	}
	defer client.Close()

	return d.processReadCommands(client, reqs)
}

func (d *Driver) processReadCommands(client *opcua.Client, reqs []sdkModel.CommandRequest) ([]*sdkModel.CommandValue, error) {
	var responses = make([]*sdkModel.CommandValue, len(reqs))

	for i, req := range reqs {
		// handle every reqs
		res, err := d.handleReadCommandRequest(client, req)
		if err != nil {
			d.Logger.Errorf("Driver.HandleReadCommands: Handle read commands failed: %v", err)
			return responses, err
		}
		responses[i] = res
	}

	return responses, nil
}

func (d *Driver) handleReadCommandRequest(deviceClient *opcua.Client, req sdkModel.CommandRequest) (*sdkModel.CommandValue, error) {
	var result = &sdkModel.CommandValue{}
	var err error

	_, isMethod := req.Attributes[METHOD]

	if isMethod {
		result, err = makeMethodCall(deviceClient, req)
		d.Logger.Infof("Method command finished: %v", result)
	} else {
		result, err = makeReadRequest(deviceClient, req)
		d.Logger.Infof("Read command finished: %v", result)
	}

	return result, err
}

func makeReadRequest(deviceClient *opcua.Client, req sdkModel.CommandRequest) (*sdkModel.CommandValue, error) {
	nodeID, err := getNodeID(req.Attributes, NODE)
	if err != nil {
		return nil, fmt.Errorf("Driver.handleReadCommands: %v", err)
	}

	id, err := ua.ParseNodeID(nodeID)
	if err != nil {
		return nil, fmt.Errorf("Driver.handleReadCommands: Invalid node id=%s; %v", nodeID, err)
	}

	request := &ua.ReadRequest{
		MaxAge: 2000,
		NodesToRead: []*ua.ReadValueID{
			{NodeID: id},
		},
		TimestampsToReturn: ua.TimestampsToReturnBoth,
	}
	resp, err := deviceClient.Read(request)
	if err != nil {
		return nil, fmt.Errorf("Driver.handleReadCommands: Read failed: %s", err)
	}
	if resp.Results[0].Status != ua.StatusOK {
		return nil, fmt.Errorf("Driver.handleReadCommands: Status not OK: %v", resp.Results[0].Status)
	}

	// make new result
	reading := resp.Results[0].Value.Value()
	return newResult(req, reading)
}

func makeMethodCall(deviceClient *opcua.Client, req sdkModel.CommandRequest) (*sdkModel.CommandValue, error) {
	var inputs []*ua.Variant

	objectID, err := getNodeID(req.Attributes, OBJECT)
	if err != nil {
		return nil, fmt.Errorf("Driver.handleReadCommands: %v", err)
	}
	oid, err := ua.ParseNodeID(objectID)
	if err != nil {
		return nil, fmt.Errorf("Driver.handleReadCommands: %v", err)
	}

	methodID, err := getNodeID(req.Attributes, METHOD)
	if err != nil {
		return nil, fmt.Errorf("Driver.handleReadCommands: %v", err)
	}
	mid, err := ua.ParseNodeID(methodID)
	if err != nil {
		return nil, fmt.Errorf("Driver.handleReadCommands: %v", err)
	}

	inputMap, ok := req.Attributes[INPUTMAP]
	if ok {
		imElements := inputMap.([]interface{})
		if len(imElements) > 0 {
			inputs = make([]*ua.Variant, len(imElements))
			for i := 0; i < len(imElements); i++ {
				inputs[i] = ua.MustVariant(imElements[i].(string))
			}
		}
	}

	request := &ua.CallMethodRequest{
		ObjectID:       oid,
		MethodID:       mid,
		InputArguments: inputs,
	}

	resp, err := deviceClient.Call(request)
	if err != nil {
		return nil, fmt.Errorf("Driver.handleReadCommands: Method call failed: %s", err)
	}
	if resp.StatusCode != ua.StatusOK {
		return nil, fmt.Errorf("Driver.handleReadCommands: Method status not OK: %v", resp.StatusCode)
	}

	return newResult(req, resp.OutputArguments[0].Value())
}

func generateCert() (*tls.Certificate, error) {

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate private key: %s", err)
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour) // 1 year

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to generate serial number: %s", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Test Client"},
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:              x509.KeyUsageContentCommitment | x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageDataEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	host := "urn:testing:client"
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = append(template.IPAddresses, ip)
	} else {
		template.DNSNames = append(template.DNSNames, host)
	}
	if uri, err := url.Parse(host); err == nil {
		template.URIs = append(template.URIs, uri)
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKeys(priv), priv)
	if err != nil {
		return nil, fmt.Errorf("failed to create certificate: %s", err)
	}

	certBuf := bytes.NewBuffer(nil)
	if err := pem.Encode(certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return nil, fmt.Errorf("failed to encode certificate: %s", err)
	}

	keyBuf := bytes.NewBuffer(nil)
	if err := pem.Encode(keyBuf, pemBlockForKeys(priv)); err != nil {
		return nil, fmt.Errorf("failed to encode key: %s", err)
	}

	cert, err := tls.X509KeyPair(certBuf.Bytes(), keyBuf.Bytes())
	return &cert, err
}

func publicKeys(priv interface{}) interface{} {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &k.PublicKey
	case *ecdsa.PrivateKey:
		return &k.PublicKey
	default:
		return nil
	}
}

func pemBlockForKeys(priv interface{}) *pem.Block {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}
	case *ecdsa.PrivateKey:
		b, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Unable to marshal ECDSA private key: %v", err)
			os.Exit(2)
		}
		return &pem.Block{Type: "EC PRIVATE KEY", Bytes: b}
	default:
		return nil
	}
}
