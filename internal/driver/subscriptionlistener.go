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
	"fmt"
	sdkModels "github.com/edgexfoundry/device-sdk-go/v2/pkg/models"
	"github.com/edgexfoundry/device-sdk-go/v2/pkg/service"
	"github.com/edgexfoundry/go-mod-core-contracts/v2/models"
	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
	"strings"
	"time"
)

func (d *Driver) startSubscriptionListener() error {

	var (
		deviceName = d.serviceConfig.OPCUAServer.DeviceName
		resources  = d.serviceConfig.OPCUAServer.Writable.Resources
	)

	// No need to start a subscription if there are no resources to monitor
	if len(resources) == 0 {
		d.Logger.Info("[Incoming listener] No resources defined to generate subscriptions.")
		return nil
	}

	// Create a cancelable context for Writable configuration
	ctxBg := context.Background()
	ctx, cancel := context.WithCancel(ctxBg)
	d.ctxCancel = cancel

	ds := service.RunningService()
	if ds == nil {
		return fmt.Errorf("[Incoming listener] unable to get running device service")
	}

	device, err := ds.GetDeviceByName(deviceName)
	if err != nil {
		return err
	}

	client, err := d.getClient(device)
	if err != nil {
		return err
	}

	if err := client.Connect(ctx); err != nil {
		d.Logger.Warnf("[Incoming listener] Failed to connect OPCUA client, %s", err)
		return err
	}
	defer client.Close()

	notifyCh := make(chan *opcua.PublishNotificationData)

	sub, err := client.SubscribeWithContext(ctx, &opcua.SubscriptionParameters{
		Interval: time.Duration(500) * time.Millisecond,
	}, notifyCh)
	if err != nil {
		return err
	}
	defer sub.Cancel(ctx)

	go GetClientState(d, client)

	if err := d.configureMonitoredItems(sub, resources, deviceName); err != nil {
		return err
	}

	// read from subscription's notification channel until ctx is cancelled
	for {
		select {
		// context return
		case <-ctx.Done():
			return nil
			// receive Publish Notification Data
		case res := <-notifyCh:
			if res.Error != nil {
				d.Logger.Debug(res.Error.Error())
				continue
			}
			switch x := res.Value.(type) {
			// result type: DateChange StatusChange
			case *ua.DataChangeNotification:
				d.handleDataChange(x)
			}
		}
	}
}

func GetClientStateAsString(connectionState opcua.ConnState) string {
	switch connectionState {
	case opcua.Closed:
		return "Closed"
	case opcua.Connecting:
		return "Connecting"
	case opcua.Connected:
		return "Connected"
	case opcua.Disconnected:
		return "Disconnected"
	case opcua.Reconnecting:
		return "Reconnecting"
	default:
		return "connection state " + fmt.Sprint(connectionState) + " is unknown"
	}
}

func GetClientState(d *Driver, client *opcua.Client) {

	// set to default values, because if client nil results to errors
	lastState := opcua.Closed
	actualState := opcua.Closed
	for {
		if client != nil {
			actualState = client.State()
			// if you are coming from connected (last state) then log warning
			// if you are in disconnected (actual state) then log warning
			if (lastState == opcua.Connected || actualState == opcua.Disconnected) && lastState != actualState {
				d.Logger.Warnf("opc ua client is in connection state %s", GetClientStateAsString(actualState))
			} else if actualState == opcua.Connected && lastState != actualState {
				d.Logger.Infof("opc ua client is in connection state %s", GetClientStateAsString(actualState))
			}
		}
		// set here the interval, maybe also variable
		time.Sleep(100 * time.Millisecond)
		lastState = actualState
	}
}

func (d *Driver) getClient(device models.Device) (*opcua.Client, error) {
	opts, err := d.createClientOptions()
	if err != nil {
		d.Logger.Warnf("Driver.getClient: Failed to create OPCUA client options, %s", err)
		return nil, err
	}

	return opcua.NewClient(d.serviceConfig.OPCUAServer.Endpoint, opts...), nil
}

func (d *Driver) configureMonitoredItems(sub *opcua.Subscription, resources, deviceName string) error {
	d.Logger.Infof("[Incoming listener] Start configuring for resources.", resources)
	ds := service.RunningService()
	if ds == nil {
		return fmt.Errorf("[Incoming listener] unable to get running device service")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	for i, node := range strings.Split(resources, ",") {
		deviceResource, ok := ds.DeviceResource(deviceName, node)
		if !ok {
			return fmt.Errorf("[Incoming listener] Unable to find device resource with name %s", node)
		}

		opcuaNodeID, err := getNodeID(deviceResource.Attributes, NODE)
		if err != nil {
			return err
		}

		id, err := ua.ParseNodeID(opcuaNodeID)
		if err != nil {
			return err
		}

		// arbitrary client handle for the monitoring item
		handle := uint32(i + 42)
		// map the client handle so we know what the value returned represents
		d.resourceMap[handle] = node
		miCreateRequest := opcua.NewMonitoredItemCreateRequestWithDefaults(id, ua.AttributeIDValue, handle)
		res, err := sub.Monitor(ua.TimestampsToReturnBoth, miCreateRequest)
		if err != nil || res.Results[0].StatusCode != ua.StatusOK {
			return err
		}

		d.Logger.Infof("[Incoming listener] Start incoming data listening for %s.", node)
	}

	return nil
}

func (d *Driver) handleDataChange(dcn *ua.DataChangeNotification) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, item := range dcn.MonitoredItems {
		data := item.Value.Value.Value()
		value := item.Value.Value
		nodeName := d.resourceMap[item.ClientHandle]
		if err := d.onIncomingDataReceived(data, nodeName, value); err != nil {
			d.Logger.Errorf("%v", err)
		}
	}
}

func (d *Driver) onIncomingDataReceived(data interface{}, nodeResourceName string, value *ua.Variant) error {
	deviceName := d.serviceConfig.OPCUAServer.DeviceName
	reading := data

	ds := service.RunningService()
	if ds == nil {
		return fmt.Errorf("[Incoming listener] unable to get running device service")
	}

	deviceResource, ok := ds.DeviceResource(deviceName, nodeResourceName)
	if !ok {
		d.Logger.Warnf("[Incoming listener] Incoming reading ignored. No DeviceObject found: name=%v deviceResource=%v value=%v", deviceName, nodeResourceName, data)
		return nil
	}

	req := sdkModels.CommandRequest{
		DeviceResourceName: nodeResourceName,
		Type:               deviceResource.Properties.ValueType,
	}

	result, err := newResult(req, reading)

	sourceTimestamp := extractSourceTimestamp(value.DataValue())
	result.Tags["source timestamp"] = sourceTimestamp.String()

	if err != nil {
		d.Logger.Warnf("[Incoming listener] Incoming reading ignored. name=%v deviceResource=%v value=%v", deviceName, nodeResourceName, data)
		return nil
	}

	asyncValues := &sdkModels.AsyncValues{
		DeviceName:    deviceName,
		CommandValues: []*sdkModels.CommandValue{result},
	}

	d.Logger.Infof("[Incoming listener] Incoming reading received: name=%v deviceResource=%v value=%v", deviceName, nodeResourceName, data)

	d.AsyncCh <- asyncValues

	return nil
}
