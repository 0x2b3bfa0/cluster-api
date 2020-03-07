/*
Copyright 2020 The Kubernetes Authors.

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
package controllers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const defaultNamespaceName = "default"

var _ = Describe("MachineHealthCheck Reconciler", func() {
	var namespace *corev1.Namespace
	var testCluster *clusterv1.Cluster

	var clusterName = "test-cluster"
	var namespaceName string

	BeforeEach(func() {
		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "mhc-test-"}}
		testCluster = &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: clusterName}}

		By("Ensuring the namespace exists")
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		namespaceName = namespace.Name

		By("Creating the Cluster")
		testCluster.Namespace = namespaceName
		Expect(k8sClient.Create(ctx, testCluster)).To(Succeed())
	})

	AfterEach(func() {
		By("Deleting any MachineHealthChecks")
		Expect(cleanupTestMachineHealthChecks(ctx, k8sClient)).To(Succeed())
		By("Deleting the Cluster")
		Expect(k8sClient.Delete(ctx, testCluster)).To(Succeed())
		By("Deleting the Namespace")
		Expect(k8sClient.Delete(ctx, namespace)).To(Succeed())

		// Ensure the cluster is actually gone before moving on
		Eventually(func() error {
			c := &clusterv1.Cluster{}
			err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespaceName, Name: clusterName}, c)
			if err != nil && apierrors.IsNotFound(err) {
				return nil
			} else if err != nil {
				return err
			}
			return errors.New("Cluster not yet deleted")
		}, timeout).Should(Succeed())
	})

	type labelTestCase struct {
		original map[string]string
		expected map[string]string
	}

	DescribeTable("should ensure the cluster-name label is correct",
		func(ltc labelTestCase) {
			By("Creating a MachineHealthCheck")
			mhcToCreate := &clusterv1.MachineHealthCheck{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-mhc",
					Namespace: namespaceName,
					Labels:    ltc.original,
				},
				Spec: clusterv1.MachineHealthCheckSpec{
					ClusterName: clusterName,
					UnhealthyConditions: []clusterv1.UnhealthyCondition{
						{
							Type:    corev1.NodeReady,
							Status:  corev1.ConditionUnknown,
							Timeout: metav1.Duration{Duration: 5 * time.Minute},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, mhcToCreate)).To(Succeed())

			Eventually(func() map[string]string {
				mhc := &clusterv1.MachineHealthCheck{}
				err := k8sClient.Get(ctx, util.ObjectKey(mhcToCreate), mhc)
				if err != nil {
					return nil
				}
				return mhc.GetLabels()
			}, timeout).Should(Equal(ltc.expected))
		},
		Entry("when no existing labels exist", labelTestCase{
			original: map[string]string{},
			expected: map[string]string{clusterv1.ClusterLabelName: clusterName},
		}),
		Entry("when the label has the wrong value", labelTestCase{
			original: map[string]string{clusterv1.ClusterLabelName: "wrong"},
			expected: map[string]string{clusterv1.ClusterLabelName: clusterName},
		}),
		Entry("without modifying other labels", labelTestCase{
			original: map[string]string{"other": "label"},
			expected: map[string]string{"other": "label", clusterv1.ClusterLabelName: clusterName},
		}),
	)

	type ownerReferenceTestCase struct {
		original []metav1.OwnerReference
		// Use a function so that runtime information can be populated (eg UID)
		expected func() []metav1.OwnerReference
	}

	DescribeTable("should ensure an owner reference is present",
		func(ortc ownerReferenceTestCase) {
			By("Creating a MachineHealthCheck")
			mhcToCreate := &clusterv1.MachineHealthCheck{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test-mhc",
					Namespace:       namespaceName,
					OwnerReferences: ortc.original,
				},
				Spec: clusterv1.MachineHealthCheckSpec{
					ClusterName: clusterName,
					UnhealthyConditions: []clusterv1.UnhealthyCondition{
						{
							Type:    corev1.NodeReady,
							Status:  corev1.ConditionUnknown,
							Timeout: metav1.Duration{Duration: 5 * time.Minute},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, mhcToCreate)).To(Succeed())

			Eventually(func() []metav1.OwnerReference {
				mhc := &clusterv1.MachineHealthCheck{}
				err := k8sClient.Get(ctx, util.ObjectKey(mhcToCreate), mhc)
				if err != nil {
					return []metav1.OwnerReference{}
				}
				return mhc.GetOwnerReferences()
			}, timeout).Should(ConsistOf(ortc.expected()))
		},
		Entry("when no existing owner references exist", ownerReferenceTestCase{
			original: []metav1.OwnerReference{},
			expected: func() []metav1.OwnerReference {
				return []metav1.OwnerReference{ownerReferenceForCluster(ctx, testCluster)}
			},
		}),
		Entry("when modifying existing owner references", ownerReferenceTestCase{
			original: []metav1.OwnerReference{{Kind: "Foo", APIVersion: "foo.bar.baz/v1", Name: "Bar", UID: "12345"}},
			expected: func() []metav1.OwnerReference {
				return []metav1.OwnerReference{{Kind: "Foo", APIVersion: "foo.bar.baz/v1", Name: "Bar", UID: "12345"}, ownerReferenceForCluster(ctx, testCluster)}
			},
		}),
	)

})

func cleanupTestMachineHealthChecks(ctx context.Context, c client.Client) error {
	mhcList := &clusterv1.MachineHealthCheckList{}
	err := c.List(ctx, mhcList)
	if err != nil {
		return err
	}
	for _, mhc := range mhcList.Items {
		m := mhc
		err = c.Delete(ctx, &m)
		if err != nil {
			return err
		}
	}
	return nil
}

func ownerReferenceForCluster(ctx context.Context, c *clusterv1.Cluster) metav1.OwnerReference {
	// Fetch the cluster to populate the UID
	cc := &clusterv1.Cluster{}
	Expect(k8sClient.Get(ctx, util.ObjectKey(c), cc)).To(Succeed())

	return metav1.OwnerReference{
		APIVersion: clusterv1.GroupVersion.String(),
		Kind:       "Cluster",
		Name:       cc.Name,
		UID:        cc.UID,
	}
}

func TestClusterToMachineHealthCheck(t *testing.T) {
	// This test sets up a proper test env to allow testing of the cache index
	// that is used as part of the clusterToMachineHealthCheck map function

	// BEGIN: Set up test environment
	g := NewWithT(t)

	testEnv = &envtest.Environment{
		CRDs: []runtime.Object{
			external.TestGenericBootstrapCRD,
			external.TestGenericBootstrapTemplateCRD,
			external.TestGenericInfrastructureCRD,
			external.TestGenericInfrastructureTemplateCRD,
		},
		CRDDirectoryPaths: []string{filepath.Join("..", "config", "crd", "bases")},
	}

	var err error
	cfg, err := testEnv.Start()
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(cfg).ToNot(BeNil())
	defer func() {
		g.Expect(testEnv.Stop()).To(Succeed())
	}()

	g.Expect(clusterv1.AddToScheme(scheme.Scheme)).To(Succeed())

	mgr, err = manager.New(cfg, manager.Options{
		Scheme:             scheme.Scheme,
		MetricsBindAddress: "0",
	})
	g.Expect(err).ToNot(HaveOccurred())

	r := &MachineHealthCheckReconciler{
		Log:    log.Log,
		Client: mgr.GetClient(),
	}
	g.Expect(r.SetupWithManager(mgr, controller.Options{})).To(Succeed())

	doneMgr := make(chan struct{})
	go func() {
		g.Expect(mgr.Start(doneMgr)).To(Succeed())
	}()
	defer close(doneMgr)

	// END: setup test environment

	namespace := defaultNamespaceName
	clusterName := "test-cluster"
	labels := make(map[string]string)

	mhc1 := newTestMachineHealthCheck("mhc1", namespace, clusterName, labels)
	mhc1Req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: mhc1.Namespace, Name: mhc1.Name}}
	mhc2 := newTestMachineHealthCheck("mhc2", namespace, clusterName, labels)
	mhc2Req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: mhc2.Namespace, Name: mhc2.Name}}
	mhc3 := newTestMachineHealthCheck("mhc3", namespace, "othercluster", labels)
	mhc4 := newTestMachineHealthCheck("mhc4", "othernamespace", clusterName, labels)
	cluster1 := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: namespace,
		},
	}

	testCases := []struct {
		name     string
		toCreate []clusterv1.MachineHealthCheck
		object   handler.MapObject
		expected []reconcile.Request
	}{
		{
			name:     "when the object passed isn't a cluster",
			toCreate: []clusterv1.MachineHealthCheck{*mhc1},
			object: handler.MapObject{
				Object: &clusterv1.Machine{},
			},
			expected: []reconcile.Request{},
		},
		{
			name:     "when a MachineHealthCheck exists for the Cluster in the same namespace",
			toCreate: []clusterv1.MachineHealthCheck{*mhc1},
			object: handler.MapObject{
				Object: cluster1,
			},
			expected: []reconcile.Request{mhc1Req},
		},
		{
			name:     "when 2 MachineHealthChecks exists for the Cluster in the same namespace",
			toCreate: []clusterv1.MachineHealthCheck{*mhc1, *mhc2},
			object: handler.MapObject{
				Object: cluster1,
			},
			expected: []reconcile.Request{mhc1Req, mhc2Req},
		},
		{
			name:     "when a MachineHealthCheck exists for another Cluster in the same namespace",
			toCreate: []clusterv1.MachineHealthCheck{*mhc3},
			object: handler.MapObject{
				Object: cluster1,
			},
			expected: []reconcile.Request{},
		},
		{
			name:     "when a MachineHealthCheck exists for another Cluster in another namespace",
			toCreate: []clusterv1.MachineHealthCheck{*mhc4},
			object: handler.MapObject{
				Object: cluster1,
			},
			expected: []reconcile.Request{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gs := NewWithT(t)

			ctx := context.Background()
			for _, obj := range tc.toCreate {
				o := obj
				gs.Expect(r.Client.Create(ctx, &o)).To(Succeed())
				defer func() {
					gs.Expect(r.Client.Delete(ctx, &o)).To(Succeed())
				}()
				// Check the cache is populated
				getObj := func() error {
					return r.Client.Get(ctx, util.ObjectKey(&o), &clusterv1.MachineHealthCheck{})
				}
				gs.Eventually(getObj, timeout).Should(Succeed())
			}

			got := r.clusterToMachineHealthCheck(tc.object)
			gs.Expect(got).To(ConsistOf(tc.expected))
		})
	}
}

func TestIndexMachineHealthCheckByClusterName(t *testing.T) {
	r := &MachineHealthCheckReconciler{
		Log: log.Log,
	}

	testCases := []struct {
		name     string
		object   runtime.Object
		expected []string
	}{
		{
			name:     "when the MachineHealthCheck has no ClusterName",
			object:   &clusterv1.MachineHealthCheck{},
			expected: []string{""},
		},
		{
			name: "when the MachineHealthCheck has a ClusterName",
			object: &clusterv1.MachineHealthCheck{
				Spec: clusterv1.MachineHealthCheckSpec{
					ClusterName: "test-cluster",
				},
			},
			expected: []string{"test-cluster"},
		},
		{
			name:     "when the object passed is not a MachineHealthCheck",
			object:   &corev1.Node{},
			expected: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			got := r.indexMachineHealthCheckByClusterName(tc.object)
			g.Expect(got).To(ConsistOf(tc.expected))
		})
	}
}

func newTestMachineHealthCheck(name, namespace, cluster string, labels map[string]string) *clusterv1.MachineHealthCheck {
	return &clusterv1.MachineHealthCheck{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: clusterv1.MachineHealthCheckSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: labels,
			},
			ClusterName: cluster,
			UnhealthyConditions: []clusterv1.UnhealthyCondition{
				{
					Type:    corev1.NodeReady,
					Status:  corev1.ConditionUnknown,
					Timeout: metav1.Duration{Duration: 5 * time.Minute},
				},
			},
		},
	}
}

func TestMachineToMachineHealthCheck(t *testing.T) {
	// This test sets up a proper test env to allow testing of the cache index
	// that is used as part of the clusterToMachineHealthCheck map function

	// BEGIN: Set up test environment
	g := NewWithT(t)

	testEnv = &envtest.Environment{
		CRDs: []runtime.Object{
			external.TestGenericBootstrapCRD,
			external.TestGenericBootstrapTemplateCRD,
			external.TestGenericInfrastructureCRD,
			external.TestGenericInfrastructureTemplateCRD,
		},
		CRDDirectoryPaths: []string{filepath.Join("..", "config", "crd", "bases")},
	}

	var err error
	cfg, err := testEnv.Start()
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(cfg).ToNot(BeNil())
	defer func() {
		g.Expect(testEnv.Stop()).To(Succeed())
	}()

	g.Expect(clusterv1.AddToScheme(scheme.Scheme)).To(Succeed())

	mgr, err = manager.New(cfg, manager.Options{
		Scheme:             scheme.Scheme,
		MetricsBindAddress: "0",
	})
	g.Expect(err).ToNot(HaveOccurred())

	r := &MachineHealthCheckReconciler{
		Log:    log.Log,
		Client: mgr.GetClient(),
	}
	g.Expect(r.SetupWithManager(mgr, controller.Options{})).To(Succeed())

	doneMgr := make(chan struct{})
	go func() {
		g.Expect(mgr.Start(doneMgr)).To(Succeed())
	}()
	defer close(doneMgr)

	// END: setup test environment

	namespace := defaultNamespaceName
	clusterName := "test-cluster"
	nodeName := "node1"
	labels := map[string]string{"cluster": "foo", "nodepool": "bar"}

	mhc1 := newTestMachineHealthCheck("mhc1", namespace, clusterName, labels)
	mhc1Req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: mhc1.Namespace, Name: mhc1.Name}}
	mhc2 := newTestMachineHealthCheck("mhc2", namespace, clusterName, labels)
	mhc2Req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: mhc2.Namespace, Name: mhc2.Name}}
	mhc3 := newTestMachineHealthCheck("mhc3", namespace, clusterName, map[string]string{"cluster": "foo", "nodepool": "other"})
	mhc4 := newTestMachineHealthCheck("mhc4", "othernamespace", clusterName, labels)
	machine1 := newTestMachine("machine1", namespace, clusterName, nodeName, labels)

	testCases := []struct {
		name     string
		toCreate []clusterv1.MachineHealthCheck
		object   handler.MapObject
		expected []reconcile.Request
	}{
		{
			name:     "when the object passed isn't a machine",
			toCreate: []clusterv1.MachineHealthCheck{*mhc1},
			object: handler.MapObject{
				Object: &clusterv1.Cluster{},
			},
			expected: []reconcile.Request{},
		},
		{
			name:     "when a MachineHealthCheck matches labels for the Machine in the same namespace",
			toCreate: []clusterv1.MachineHealthCheck{*mhc1},
			object: handler.MapObject{
				Object: machine1,
			},
			expected: []reconcile.Request{mhc1Req},
		},
		{
			name:     "when 2 MachineHealthChecks match labels for the Machine in the same namespace",
			toCreate: []clusterv1.MachineHealthCheck{*mhc1, *mhc2},
			object: handler.MapObject{
				Object: machine1,
			},
			expected: []reconcile.Request{mhc1Req, mhc2Req},
		},
		{
			name:     "when a MachineHealthCheck does not match labels for the Machine in the same namespace",
			toCreate: []clusterv1.MachineHealthCheck{*mhc3},
			object: handler.MapObject{
				Object: machine1,
			},
			expected: []reconcile.Request{},
		},
		{
			name:     "when a MachineHealthCheck matches labels for the Machine in another namespace",
			toCreate: []clusterv1.MachineHealthCheck{*mhc4},
			object: handler.MapObject{
				Object: machine1,
			},
			expected: []reconcile.Request{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gs := NewWithT(t)

			ctx := context.Background()
			for _, obj := range tc.toCreate {
				o := obj
				gs.Expect(r.Client.Create(ctx, &o)).To(Succeed())
				defer func() {
					gs.Expect(r.Client.Delete(ctx, &o)).To(Succeed())
				}()
				// Check the cache is populated
				getObj := func() error {
					return r.Client.Get(ctx, util.ObjectKey(&o), &clusterv1.MachineHealthCheck{})
				}
				gs.Eventually(getObj, timeout).Should(Succeed())
			}

			got := r.machineToMachineHealthCheck(tc.object)
			gs.Expect(got).To(ConsistOf(tc.expected))
		})
	}
}

func TestNodeToMachineHealthCheck(t *testing.T) {
	// This test sets up a proper test env to allow testing of the cache index
	// that is used as part of the clusterToMachineHealthCheck map function

	// BEGIN: Set up test environment
	g := NewWithT(t)

	testEnv = &envtest.Environment{
		CRDs: []runtime.Object{
			external.TestGenericBootstrapCRD,
			external.TestGenericBootstrapTemplateCRD,
			external.TestGenericInfrastructureCRD,
			external.TestGenericInfrastructureTemplateCRD,
		},
		CRDDirectoryPaths: []string{filepath.Join("..", "config", "crd", "bases")},
	}

	var err error
	cfg, err := testEnv.Start()
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(cfg).ToNot(BeNil())
	defer func() {
		g.Expect(testEnv.Stop()).To(Succeed())
	}()

	g.Expect(clusterv1.AddToScheme(scheme.Scheme)).To(Succeed())

	mgr, err = manager.New(cfg, manager.Options{
		Scheme:             scheme.Scheme,
		MetricsBindAddress: "0",
	})
	g.Expect(err).ToNot(HaveOccurred())

	r := &MachineHealthCheckReconciler{
		Log:    log.Log,
		Client: mgr.GetClient(),
	}
	g.Expect(r.SetupWithManager(mgr, controller.Options{})).To(Succeed())

	doneMgr := make(chan struct{})
	go func() {
		g.Expect(mgr.Start(doneMgr)).To(Succeed())
	}()
	defer close(doneMgr)

	// END: setup test environment

	namespace := defaultNamespaceName
	clusterName := "test-cluster"
	nodeName := "node1"
	labels := map[string]string{"cluster": "foo", "nodepool": "bar"}

	mhc1 := newTestMachineHealthCheck("mhc1", namespace, clusterName, labels)
	mhc1Req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: mhc1.Namespace, Name: mhc1.Name}}
	mhc2 := newTestMachineHealthCheck("mhc2", namespace, clusterName, labels)
	mhc2Req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: mhc2.Namespace, Name: mhc2.Name}}
	mhc3 := newTestMachineHealthCheck("mhc3", namespace, "othercluster", labels)
	mhc4 := newTestMachineHealthCheck("mhc4", "othernamespace", clusterName, labels)

	machine1 := newTestMachine("machine1", namespace, clusterName, nodeName, labels)
	machine2 := newTestMachine("machine2", namespace, clusterName, nodeName, labels)

	node1 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
	}

	testCases := []struct {
		name        string
		mhcToCreate []clusterv1.MachineHealthCheck
		mToCreate   []clusterv1.Machine
		object      handler.MapObject
		expected    []reconcile.Request
	}{
		{
			name:        "when the object passed isn't a Node",
			mhcToCreate: []clusterv1.MachineHealthCheck{*mhc1},
			mToCreate:   []clusterv1.Machine{*machine1},
			object: handler.MapObject{
				Object: &clusterv1.Machine{},
			},
			expected: []reconcile.Request{},
		},
		{
			name:        "when no Machine exists for the Node",
			mhcToCreate: []clusterv1.MachineHealthCheck{*mhc1},
			mToCreate:   []clusterv1.Machine{},
			object: handler.MapObject{
				Object: node1,
			},
			expected: []reconcile.Request{},
		},
		{
			name:        "when two Machines exist for the Node",
			mhcToCreate: []clusterv1.MachineHealthCheck{*mhc1},
			mToCreate:   []clusterv1.Machine{*machine1, *machine2},
			object: handler.MapObject{
				Object: node1,
			},
			expected: []reconcile.Request{},
		},
		{
			name:        "when no MachineHealthCheck exists for the Node in the Machine's namespace",
			mhcToCreate: []clusterv1.MachineHealthCheck{*mhc4},
			mToCreate:   []clusterv1.Machine{*machine1},
			object: handler.MapObject{
				Object: node1,
			},
			expected: []reconcile.Request{},
		},
		{
			name:        "when a MachineHealthCheck exists for the Node in the Machine's namespace",
			mhcToCreate: []clusterv1.MachineHealthCheck{*mhc1},
			mToCreate:   []clusterv1.Machine{*machine1},
			object: handler.MapObject{
				Object: node1,
			},
			expected: []reconcile.Request{mhc1Req},
		},
		{
			name:        "when two MachineHealthChecks exist for the Node in the Machine's namespace",
			mhcToCreate: []clusterv1.MachineHealthCheck{*mhc1, *mhc2},
			mToCreate:   []clusterv1.Machine{*machine1},
			object: handler.MapObject{
				Object: node1,
			},
			expected: []reconcile.Request{mhc1Req, mhc2Req},
		},
		{
			name:        "when a MachineHealthCheck exists for the Node, but not in the Machine's namespace",
			mhcToCreate: []clusterv1.MachineHealthCheck{*mhc3},
			mToCreate:   []clusterv1.Machine{*machine1},
			object: handler.MapObject{
				Object: node1,
			},
			expected: []reconcile.Request{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gs := NewWithT(t)

			ctx := context.Background()
			for _, obj := range tc.mhcToCreate {
				o := obj
				gs.Expect(r.Client.Create(ctx, &o)).To(Succeed())
				defer func() {
					gs.Expect(r.Client.Delete(ctx, &o)).To(Succeed())
				}()
				// Check the cache is populated
				key := util.ObjectKey(&o)
				getObj := func() error {
					return r.Client.Get(ctx, key, &clusterv1.MachineHealthCheck{})
				}
				gs.Eventually(getObj, timeout).Should(Succeed())
			}
			for _, obj := range tc.mToCreate {
				o := obj
				gs.Expect(r.Client.Create(ctx, &o)).To(Succeed())
				defer func() {
					gs.Expect(r.Client.Delete(ctx, &o)).To(Succeed())
				}()
				// Ensure the status is set (required for matching node to machine)
				o.Status = obj.Status
				gs.Expect(r.Client.Status().Update(ctx, &o)).To(Succeed())

				// Check the cache is up to date with the status update
				key := util.ObjectKey(&o)
				checkStatus := func() clusterv1.MachineStatus {
					m := &clusterv1.Machine{}
					err := r.Client.Get(ctx, key, m)
					if err != nil {
						return clusterv1.MachineStatus{}
					}
					return m.Status
				}
				gs.Eventually(checkStatus, timeout).Should(Equal(o.Status))
			}

			got := r.nodeToMachineHealthCheck(tc.object)
			gs.Expect(got).To(ConsistOf(tc.expected))
		})
	}
}

func TestIndexMachineByNodeName(t *testing.T) {
	r := &MachineHealthCheckReconciler{
		Log: log.Log,
	}

	testCases := []struct {
		name     string
		object   runtime.Object
		expected []string
	}{
		{
			name:     "when the machine has no NodeRef",
			object:   &clusterv1.Machine{},
			expected: []string{},
		},
		{
			name: "when the machine has valid a NodeRef",
			object: &clusterv1.Machine{
				Status: clusterv1.MachineStatus{
					NodeRef: &corev1.ObjectReference{
						Name: "node1",
					},
				},
			},
			expected: []string{"node1"},
		},
		{
			name:     "when the object passed is not a Machine",
			object:   &corev1.Node{},
			expected: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			got := r.indexMachineByNodeName(tc.object)
			g.Expect(got).To(ConsistOf(tc.expected))
		})
	}
}

func TestIsAllowedRedmediation(t *testing.T) {
	testCases := []struct {
		name             string
		maxUnhealthy     *intstr.IntOrString
		expectedMachines int32
		currentHealthy   int32
		allowed          bool
	}{
		{
			name:             "when maxUnhealthy is not set",
			maxUnhealthy:     nil,
			expectedMachines: int32(3),
			currentHealthy:   int32(0),
			allowed:          true,
		},
		{
			name:             "when maxUnhealthy is not an int or percentage",
			maxUnhealthy:     &intstr.IntOrString{Type: intstr.String, StrVal: "abcdef"},
			expectedMachines: int32(5),
			currentHealthy:   int32(2),
			allowed:          false,
		},
		{
			name:             "when maxUnhealthy is an int less than current unhealthy",
			maxUnhealthy:     &intstr.IntOrString{Type: intstr.Int, IntVal: int32(1)},
			expectedMachines: int32(3),
			currentHealthy:   int32(1),
			allowed:          false,
		},
		{
			name:             "when maxUnhealthy is an int equal to current unhealthy",
			maxUnhealthy:     &intstr.IntOrString{Type: intstr.Int, IntVal: int32(2)},
			expectedMachines: int32(3),
			currentHealthy:   int32(1),
			allowed:          true,
		},
		{
			name:             "when maxUnhealthy is an int greater than current unhealthy",
			maxUnhealthy:     &intstr.IntOrString{Type: intstr.Int, IntVal: int32(3)},
			expectedMachines: int32(3),
			currentHealthy:   int32(1),
			allowed:          true,
		},
		{
			name:             "when maxUnhealthy is a percentage less than current unhealthy",
			maxUnhealthy:     &intstr.IntOrString{Type: intstr.String, StrVal: "50%"},
			expectedMachines: int32(5),
			currentHealthy:   int32(2),
			allowed:          false,
		},
		{
			name:             "when maxUnhealthy is a percentage equal to current unhealthy",
			maxUnhealthy:     &intstr.IntOrString{Type: intstr.String, StrVal: "60%"},
			expectedMachines: int32(5),
			currentHealthy:   int32(2),
			allowed:          true,
		},
		{
			name:             "when maxUnhealthy is a percentage greater than current unhealthy",
			maxUnhealthy:     &intstr.IntOrString{Type: intstr.String, StrVal: "70%"},
			expectedMachines: int32(5),
			currentHealthy:   int32(2),
			allowed:          true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			mhc := &clusterv1.MachineHealthCheck{
				Spec: clusterv1.MachineHealthCheckSpec{
					MaxUnhealthy: tc.maxUnhealthy,
				},
				Status: clusterv1.MachineHealthCheckStatus{
					ExpectedMachines: tc.expectedMachines,
					CurrentHealthy:   tc.currentHealthy,
				},
			}

			g.Expect(isAllowedRemediation(mhc)).To(Equal(tc.allowed))
		})
	}
}
