/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package webhook registers all webhooks for the workbenches operator.
package webhook

import (
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hardwareprofilewebhook "github.com/opendatahub-io/workbenches-operator/internal/webhook/hardwareprofile"
	notebookwebhook "github.com/opendatahub-io/workbenches-operator/internal/webhook/notebook"
)

var webhookLog = logf.Log.WithName("webhook-registration")

type webhookEntry struct {
	name     string
	register func(ctrl.Manager) error
}

// RegisterAllWebhooks registers all webhook setup functions with the given manager.
func RegisterAllWebhooks(mgr ctrl.Manager) error {
	entries := []webhookEntry{
		{name: "notebook-connection", register: notebookwebhook.RegisterWebhooks},
		{name: "hardwareprofile", register: hardwareprofilewebhook.RegisterWebhooks},
	}

	for _, e := range entries {
		webhookLog.Info("registering webhook", "name", e.name)

		if err := e.register(mgr); err != nil {
			return fmt.Errorf("failed to register webhook %s: %w", e.name, err)
		}
	}

	return nil
}
