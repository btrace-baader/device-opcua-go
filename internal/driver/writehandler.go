// -*- Mode: Go; indent-tabs-mode: t -*-
//
// Copyright (C) 2018 Canonical Ltd
// Copyright (C) 2018 IOTech Ltd
// Copyright (C) 2021 Schneider Electric
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"context"
	"crypto/rsa"
	"fmt"
	"log"
	"time"

	"github.com/edgexfoundry/device-opcua-go/internal/config"
	sdkModel "github.com/edgexfoundry/device-sdk-go/v2/pkg/models"
	"github.com/edgexfoundry/go-mod-core-contracts/v2/common"
	"github.com/edgexfoundry/go-mod-core-contracts/v2/models"
	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// HandleWriteCommands passes a slice of CommandRequest struct each representing
// a ResourceOperation for a specific device resource (aka DeviceObject).
// Since the commands are actuation commands, params provide parameters for the individual
// command.
func (d *Driver) HandleWriteCommands(deviceName string, protocols map[string]models.ProtocolProperties,
	reqs []sdkModel.CommandRequest, params []*sdkModel.CommandValue) error {

	d.Logger.Debugf("Driver.HandleWriteCommands: protocols: %v, resource: %v, parameters: %v", protocols, reqs[0].DeviceResourceName, params)
	var err error

	// create device client and open connection
	endpoint, err := config.FetchEndpoint(protocols)
	if err != nil {
		return err
	}

	endpoints, err := opcua.GetEndpoints(endpoint)
	if err != nil {
		d.Logger.Error("OPC GetEndpoints: %w", err)
	}
	credentials, err := getCredentials(d.serviceConfig.OPCUAServer.CredentialsPath)
	if err != nil {
		d.Logger.Error("getCredentials: %w", err)
	}

	username := credentials.Username
	password := credentials.Password
	policy := ua.SecurityPolicyURIBasic256Sha256
	mode := ua.MessageSecurityModeSignAndEncrypt

	ep := opcua.SelectEndpoint(endpoints, policy, mode)
	c, err := generateCert() // This is where you generate the certificate
	if err != nil {
		d.Logger.Error("generateCert: %w", err)
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
		opcua.AuthUsername(username, password), // Use this if you are using username and password
		opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeUserName),
		opcua.SessionTimeout(30 * time.Minute),
	}
	ctx := context.Background()
	client := opcua.NewClient(d.serviceConfig.OPCUAServer.Endpoint, opts...)
	if err := client.Connect(ctx); err != nil {
		d.Logger.Warnf("Driver.HandleWriteCommands: Failed to connect OPCUA client, %s", err)
		return err
	}
	defer client.Close()

	return d.processWriteCommands(client, reqs, params)
}

func (d *Driver) processWriteCommands(client *opcua.Client, reqs []sdkModel.CommandRequest, params []*sdkModel.CommandValue) error {
	for i, req := range reqs {
		err := d.handleWriteCommandRequest(client, req, params[i])
		if err != nil {
			d.Logger.Errorf("Driver.HandleWriteCommands: Handle write commands failed: %v", err)
			return err
		}
	}

	return nil
}

func (d *Driver) handleWriteCommandRequest(deviceClient *opcua.Client, req sdkModel.CommandRequest,
	param *sdkModel.CommandValue) error {
	nodeID, err := getNodeID(req.Attributes, NODE)
	if err != nil {
		return fmt.Errorf("Driver.handleWriteCommands: %v", err)
	}

	// get NewNodeID
	id, err := ua.ParseNodeID(nodeID)
	if err != nil {
		return fmt.Errorf("Driver.handleWriteCommands: Invalid node id=%s", nodeID)
	}

	value, err := newCommandValue(req.Type, param)
	if err != nil {
		return err
	}

	v, err := ua.NewVariant(value)
	if err != nil {
		return fmt.Errorf("Driver.handleWriteCommands: invalid value: %v", err)
	}

	request := &ua.WriteRequest{
		NodesToWrite: []*ua.WriteValue{
			{
				NodeID:      id,
				AttributeID: ua.AttributeIDValue,
				Value: &ua.DataValue{
					EncodingMask: ua.DataValueValue, // encoding mask
					Value:        v,
				},
			},
		},
	}

	resp, err := deviceClient.Write(request)
	if err != nil {
		d.Logger.Errorf("Driver.handleWriteCommands: Write value %v failed: %s", v, err)
		return err
	}
	d.Logger.Infof("Driver.handleWriteCommands: write sucessfully, %v", resp.Results[0])
	return nil
}

func newCommandValue(valueType string, param *sdkModel.CommandValue) (interface{}, error) {
	var commandValue interface{}
	var err error
	switch valueType {
	case common.ValueTypeBool:
		commandValue, err = param.BoolValue()
	case common.ValueTypeString:
		commandValue, err = param.StringValue()
	case common.ValueTypeUint8:
		commandValue, err = param.Uint8Value()
	case common.ValueTypeUint16:
		commandValue, err = param.Uint16Value()
	case common.ValueTypeUint32:
		commandValue, err = param.Uint32Value()
	case common.ValueTypeUint64:
		commandValue, err = param.Uint64Value()
	case common.ValueTypeInt8:
		commandValue, err = param.Int8Value()
	case common.ValueTypeInt16:
		commandValue, err = param.Int16Value()
	case common.ValueTypeInt32:
		commandValue, err = param.Int32Value()
	case common.ValueTypeInt64:
		commandValue, err = param.Int64Value()
	case common.ValueTypeFloat32:
		commandValue, err = param.Float32Value()
	case common.ValueTypeFloat64:
		commandValue, err = param.Float64Value()
	default:
		err = fmt.Errorf("fail to convert param, none supported value type: %v", valueType)
	}

	return commandValue, err
}
