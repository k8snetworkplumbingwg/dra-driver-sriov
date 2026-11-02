/*
 * Copyright 2026 The dra-driver-sriov Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package nri

import (
	"context"
	"net"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/net/multiplex"
	"github.com/containerd/nri/pkg/stub"
	"github.com/containerd/ttrpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

// registrationRuntime tests the wire exchange, not runtime timeout enforcement.
// An older runtime can configure the plugin while ignoring the new response field.
type registrationRuntime struct {
	client  api.PluginService
	version string
	policy  chan *api.FailurePolicy
}

func (r *registrationRuntime) RegisterPlugin(ctx context.Context, _ *api.RegisterPluginRequest) (*api.Empty, error) {
	response, err := r.client.Configure(ctx, &api.ConfigureRequest{
		RuntimeName: "containerd", RuntimeVersion: "2.2.0",
		NRIVersion: r.version, RegistrationTimeout: 5000, RequestTimeout: 2000,
	})
	if err != nil {
		return nil, err
	}
	r.policy <- response.FailurePolicy
	return &api.Empty{}, nil
}

func (*registrationRuntime) UpdateContainers(context.Context, *api.UpdateContainersRequest) (*api.UpdateContainersResponse, error) {
	return &api.UpdateContainersResponse{}, nil
}

var _ = Describe("NRI registration", func() {
	DescribeTable("advertises failure policy and cancels on real connection loss",
		func(version string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			DeferCleanup(cancel)
			mainCtx, cancelMain := context.WithCancelCause(ctx)
			DeferCleanup(func() { cancelMain(nil) })

			pluginConn, runtimeConn := net.Pipe()
			DeferCleanup(func() { _ = pluginConn.Close() })
			mux := multiplex.Multiplex(runtimeConn)
			DeferCleanup(func() { _ = mux.Close() })
			listener, err := mux.Listen(multiplex.RuntimeServiceConn)
			Expect(err).NotTo(HaveOccurred())
			conn, err := mux.Open(multiplex.PluginServiceConn)
			Expect(err).NotTo(HaveOccurred())
			client := ttrpc.NewClient(conn)
			DeferCleanup(func() { _ = client.Close() })
			runtime := &registrationRuntime{
				client:  api.NewPluginClient(client),
				version: version,
				policy:  make(chan *api.FailurePolicy, 1),
			}
			server, err := ttrpc.NewServer()
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = server.Close() })
			api.RegisterRuntimeService(server, runtime)
			served := make(chan error, 1)
			go func() { served <- server.Serve(ctx, listener) }()
			DeferCleanup(func() {
				_ = mux.Close()
				_ = listener.Close()
				Eventually(served, 5*time.Second).Should(Receive())
			})

			plugin, err := newNRIPlugin(&types.Config{
				Flags: &types.Flags{}, CancelMainCtx: cancelMain,
			}, nil, nil, nil, stub.WithConnection(pluginConn),
				stub.WithPluginName("sriov"), stub.WithPluginIdx("10"))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(plugin.stub.Stop)
			// Use the real stub without starting the unrelated metadata worker.
			Expect(plugin.stub.Start(ctx)).To(Succeed())
			var policy *api.FailurePolicy
			Eventually(runtime.policy, 5*time.Second).Should(Receive(&policy))
			Expect(policy).NotTo(BeNil())
			Expect(policy.FailRequest).To(BeTrue())
			Expect(policy.KeepConnected).To(BeTrue())
			Expect(mainCtx.Err()).NotTo(HaveOccurred())

			Expect(mux.Close()).To(Succeed())
			Eventually(mainCtx.Done(), 5*time.Second).Should(BeClosed())
			Expect(context.Cause(mainCtx)).To(MatchError("NRI plugin closed"))
		},
		Entry("supporting runtime", "0.12.4"),
		Entry("older runtime ignoring policy", "0.12.3"),
	)
})
