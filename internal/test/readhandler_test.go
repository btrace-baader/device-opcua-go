// -*- Mode: Go; indent-tabs-mode: t -*-
//
// Copyright (C) 2021 Schneider Electric
//
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"github.com/edgexfoundry/device-opcua-go/internal/config"
	"github.com/edgexfoundry/device-opcua-go/internal/driver"
	sdkModel "github.com/edgexfoundry/device-sdk-go/v2/pkg/models"
	"github.com/edgexfoundry/go-mod-core-contracts/v2/clients/logger"
	"github.com/edgexfoundry/go-mod-core-contracts/v2/common"
	"github.com/edgexfoundry/go-mod-core-contracts/v2/models"
	"github.com/gopcua/opcua"
	"reflect"
	"testing"
)

func TestDriverHandleReadCommands(t *testing.T) {
	type args struct {
		deviceName string
		protocols  map[string]models.ProtocolProperties
		reqs       []sdkModel.CommandRequest
	}
	tests := []struct {
		name          string
		args          args
		serviceConfig config.ServiceConfig
		want          []*sdkModel.CommandValue
		wantErr       bool
	}{
		{
			name: "NOK - no endpoint defined",
			args: args{
				deviceName: "Test1",
				protocols:  map[string]models.ProtocolProperties{config.Protocol: {}},
				reqs:       []sdkModel.CommandRequest{{DeviceResourceName: "TestVar1"}},
			},
			serviceConfig: config.ServiceConfig{OPCUAServer: config.OPCUAServerConfig{Endpoint: ""}},
			want:          nil,
			wantErr:       true,
		},
		{
			name: "NOK - invalid endpoint defined",
			args: args{
				deviceName: "Test2",
				protocols: map[string]models.ProtocolProperties{
					config.Protocol: {config.Endpoint: Protocol + "unknown"},
				},
				reqs: []sdkModel.CommandRequest{{DeviceResourceName: "TestVar1"}},
			},
			serviceConfig: config.ServiceConfig{OPCUAServer: config.OPCUAServerConfig{Endpoint: ""}},
			want:          nil,
			wantErr:       true,
		},
		{
			name: "NOK - non-existent variable",
			args: args{
				deviceName: "Test3",
				protocols: map[string]models.ProtocolProperties{
					config.Protocol: {config.Endpoint: Protocol + Address},
				},
				reqs: []sdkModel.CommandRequest{{
					DeviceResourceName: "TestVar1",
					Attributes:         map[string]interface{}{driver.NODE: "ns=2;s=fake"},
					Type:               common.ValueTypeInt32,
				}},
			},
			serviceConfig: config.ServiceConfig{OPCUAServer: config.OPCUAServerConfig{Endpoint: Protocol + Address}},
			want:          make([]*sdkModel.CommandValue, 1),
			wantErr:       true,
		},
		{
			name: "NOK - read command - invalid node id",
			args: args{
				deviceName: "Test4",
				protocols: map[string]models.ProtocolProperties{
					config.Protocol: {config.Endpoint: Protocol + Address},
				},
				reqs: []sdkModel.CommandRequest{{
					DeviceResourceName: "TestResource1",
					Attributes:         map[string]interface{}{driver.NODE: "2"},
					Type:               common.ValueTypeInt32,
				}},
			},
			serviceConfig: config.ServiceConfig{OPCUAServer: config.OPCUAServerConfig{Endpoint: Protocol + Address}},
			want:          make([]*sdkModel.CommandValue, 1),
			wantErr:       true,
		},
		{
			name: "NOK - method call - invalid node id",
			args: args{
				deviceName: "Test5",
				protocols: map[string]models.ProtocolProperties{
					config.Protocol: {config.Endpoint: Protocol + Address},
				},
				reqs: []sdkModel.CommandRequest{{
					DeviceResourceName: "TestResource1",
					Attributes:         map[string]interface{}{driver.METHOD: "ns=2;s=test"},
					Type:               common.ValueTypeInt32,
				}},
			},
			serviceConfig: config.ServiceConfig{OPCUAServer: config.OPCUAServerConfig{Endpoint: Protocol + Address}},
			want:          make([]*sdkModel.CommandValue, 1),
			wantErr:       true,
		},
		{
			name: "NOK - method call - method does not exist",
			args: args{
				deviceName: "Test6",
				protocols: map[string]models.ProtocolProperties{
					config.Protocol: {config.Endpoint: Protocol + Address},
				},
				reqs: []sdkModel.CommandRequest{{
					DeviceResourceName: "TestResource1",
					Attributes:         map[string]interface{}{driver.METHOD: "ns=2;s=test", driver.OBJECT: "ns=2;s=main"},
					Type:               common.ValueTypeInt32,
				}},
			},
			serviceConfig: config.ServiceConfig{OPCUAServer: config.OPCUAServerConfig{Endpoint: Protocol + Address}},
			want:          make([]*sdkModel.CommandValue, 1),
			wantErr:       true,
		},
		{
			name: "OK - read value from mock server",
			args: args{
				deviceName: "Test7",
				protocols: map[string]models.ProtocolProperties{
					config.Protocol: {config.Endpoint: Protocol + Address},
				},
				reqs: []sdkModel.CommandRequest{{
					DeviceResourceName: "TestVar1",
					Attributes:         map[string]interface{}{driver.NODE: "ns=2;s=ro_int32"},
					Type:               common.ValueTypeInt32,
				}},
			},
			want: []*sdkModel.CommandValue{{
				DeviceResourceName: "TestVar1",
				Type:               common.ValueTypeInt32,
				Value:              int32(5),
				Tags:               make(map[string]string),
			}},
			serviceConfig: config.ServiceConfig{OPCUAServer: config.OPCUAServerConfig{Endpoint: Protocol + Address}},
			wantErr:       false,
		},
		{
			name: "OK - call method from mock server",
			args: args{
				deviceName: "Test8",
				protocols: map[string]models.ProtocolProperties{
					config.Protocol: {config.Endpoint: Protocol + Address},
				},
				reqs: []sdkModel.CommandRequest{{
					DeviceResourceName: "SquareResource",
					Attributes:         map[string]interface{}{driver.METHOD: "ns=2;s=square", driver.OBJECT: "ns=2;s=main", driver.INPUTMAP: []interface{}{"2"}},
					Type:               common.ValueTypeInt64,
				}},
			},
			want: []*sdkModel.CommandValue{{
				DeviceResourceName: "SquareResource",
				Type:               common.ValueTypeInt64,
				Value:              int64(4),
				Tags:               make(map[string]string),
			}},
			serviceConfig: config.ServiceConfig{OPCUAServer: config.OPCUAServerConfig{Endpoint: Protocol + Address}},
			wantErr:       false,
		},
	}

	server := NewServer("../test/opcua_server.py")
	defer func(server *Server) {
		err := server.Close()
		if err != nil {
			// do nothing
		}
	}(server)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &driver.Driver{
				Logger: &logger.MockLogger{},
			}
			d.ServiceConfig = &tt.serviceConfig

			// mock client options creation here since it is the same for every test
			driver.CreateClientOptions = func() ([]opcua.Option, error) {
				var opts []opcua.Option
				return opts, nil
			}
			got, err := d.HandleReadCommands(tt.args.deviceName, tt.args.protocols, tt.args.reqs)
			if (err != nil) != tt.wantErr {
				t.Errorf("Driver.HandleReadCommands() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			// Ignore Origin and source timestamp for DeepEqual
			if len(got) > 0 && got[0] != nil {
				got[0].Origin = 0
				got[0].Tags = map[string]string{}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Driver.HandleReadCommands() = %v, want %v", got, tt.want)
			}
		})
	}
}
