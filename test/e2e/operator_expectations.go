package main

import (
	"time"

	"context"
	"errors"
	"fmt"
	"github.com/golang/glog"
	osconfigv1 "github.com/openshift/api/config/v1"
	cvoresourcemerge "github.com/openshift/cluster-version-operator/lib/resourcemerge"
	kappsapi "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	capiv1alpha1 "sigs.k8s.io/cluster-api/pkg/apis/cluster/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	waitShort  = 1 * time.Minute
	waitMedium = 3 * time.Minute
	waitLong   = 10 * time.Minute
)

func (tc *testConfig) ExpectOperatorAvailable() error {
	name := "machine-api-operator"
	key := types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}
	d := &kappsapi.Deployment{}

	err := wait.PollImmediate(1*time.Second, waitShort, func() (bool, error) {
		if err := tc.client.Get(context.TODO(), key, d); err != nil {
			glog.Errorf("error querying api for Deployment object: %v, retrying...", err)
			return false, nil
		}
		if d.Status.ReadyReplicas < 1 {
			return false, nil
		}
		return true, nil
	})
	return err
}

func (tc *testConfig) ExpectOneClusterObject() error {
	listOptions := client.ListOptions{
		Namespace: namespace,
	}
	clusterList := capiv1alpha1.ClusterList{}

	err := wait.PollImmediate(1*time.Second, waitShort, func() (bool, error) {
		if err := tc.client.List(context.TODO(), &listOptions, &clusterList); err != nil {
			glog.Errorf("error querying api for clusterList object: %v, retrying...", err)
			return false, nil
		}
		if len(clusterList.Items) != 1 {
			return false, errors.New("more than one cluster object found")
		}
		return true, nil
	})
	return err
}

func (tc *testConfig) ExpectClusterOperatorStatusAvailable() error {
	name := "machine-api-operator"
	key := types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}
	clusterOperator := &osconfigv1.ClusterOperator{}

	err := wait.PollImmediate(1*time.Second, waitShort, func() (bool, error) {
		if err := tc.client.Get(context.TODO(), key, clusterOperator); err != nil {
			glog.Errorf("error querying api for OperatorStatus object: %v, retrying...", err)
			return false, nil
		}
		if available := cvoresourcemerge.FindOperatorStatusCondition(clusterOperator.Status.Conditions, osconfigv1.OperatorAvailable); available != nil {
			if available.Status == osconfigv1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	return err
}

func (tc *testConfig) ExpectAllMachinesLinkedToANode() error {
	machineAnnotationKey := "machine"
	listOptions := client.ListOptions{
		Namespace: namespace,
	}
	machineList := capiv1alpha1.MachineList{}
	nodeList := corev1.NodeList{}

	err := wait.PollImmediate(1*time.Second, waitShort, func() (bool, error) {
		if err := tc.client.List(context.TODO(), &listOptions, &machineList); err != nil {
			glog.Errorf("error querying api for machineList object: %v, retrying...", err)
			return false, nil
		}
		if err := tc.client.List(context.TODO(), &listOptions, &nodeList); err != nil {
			glog.Errorf("error querying api for nodeList object: %v, retrying...", err)
			return false, nil
		}
		glog.Infof("Waiting for %d machines to become nodes", len(machineList.Items))
		return len(machineList.Items) == len(nodeList.Items), nil
	})
	if err != nil {
		return err
	}

	return wait.PollImmediate(1*time.Second, waitShort, func() (bool, error) {
		nodeNameToMachineAnnotation := make(map[string]string)
		for _, node := range nodeList.Items {
			nodeNameToMachineAnnotation[node.Name] = node.Annotations[machineAnnotationKey]
		}
		for _, machine := range machineList.Items {
			if machine.Status.NodeRef == nil {
				glog.Errorf("machine %s has no NodeRef, retrying...", machine.Name)
				return false, nil
			}
			nodeName := machine.Status.NodeRef.Name
			if nodeNameToMachineAnnotation[nodeName] != fmt.Sprintf("%s/%s", namespace, machine.Name) {
				glog.Errorf("node name %s does not match expected machine name %s, retrying...", nodeName, machine.Name)
				return false, nil
			}
		}
		return true, nil
	})
}

func (tc *testConfig) ExpectReconcileControllersDeployment() error {
	key := types.NamespacedName{
		Namespace: namespace,
		Name:      "clusterapi-manager-controllers",
	}
	d := &kappsapi.Deployment{}

	glog.Info("Get deployment")
	err := wait.PollImmediate(1*time.Second, waitShort, func() (bool, error) {
		if err := tc.client.Get(context.TODO(), key, d); err != nil {
			glog.Errorf("error querying api for Deployment object: %v, retrying...", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return err
	}

	glog.Info("Delete deployment")
	err = wait.PollImmediate(1*time.Second, waitShort, func() (bool, error) {
		if err := tc.client.Delete(context.TODO(), d); err != nil {
			glog.Errorf("error querying api for Deployment object: %v, retrying...", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return err
	}

	glog.Info("Verify deployment is recreated")
	err = wait.PollImmediate(1*time.Second, waitLong, func() (bool, error) {
		if err := tc.client.Get(context.TODO(), key, d); err != nil {
			glog.Errorf("error querying api for Deployment object: %v, retrying...", err)
			return false, nil
		}
		if d.Status.ReadyReplicas < 1 || !d.DeletionTimestamp.IsZero() {
			return false, nil
		}
		return true, nil
	})
	return err
}

func (tc *testConfig) ExpectAdditiveReconcileMachineTaints() error {
	glog.Info("Verify machine taints are getting applied to node")
	listOptions := client.ListOptions{
		Namespace: namespace,
	}
	machineList := capiv1alpha1.MachineList{}

	if err := tc.client.List(context.TODO(), &listOptions, &machineList); err != nil {
		return fmt.Errorf("error querying api for machineList object: %v", err)

	}
	glog.Info("Got the machine list")
	machine := machineList.Items[0]
	if machine.Status.NodeRef == nil {
		return fmt.Errorf("machine %s has no NodeRef", machine.Name)
	}
	glog.Infof("Got the machine, %s", machine.Name)
	nodeName := machine.Status.NodeRef.Name
	nodeKey := types.NamespacedName{
		Namespace: namespace,
		Name:      nodeName,
	}
	node := &corev1.Node{}

	if err := tc.client.Get(context.TODO(), nodeKey, node); err != nil {
		return fmt.Errorf("error querying api for node object: %v", err)
	}
	glog.Infof("Got the node, %s, from machine, %s", node.Name, machine.Name)
	nodeTaint := corev1.Taint{
		Key:    "not-from-machine",
		Value:  "true",
		Effect: corev1.TaintEffectNoSchedule,
	}
	node.Spec.Taints = []corev1.Taint{nodeTaint}
	if err := tc.client.Update(context.TODO(), node); err != nil {
		return fmt.Errorf("error updating node object with non-machine taint: %v", err)
	}
	glog.Info("Updated node object with taint")
	machineTaint := corev1.Taint{
		Key:    "from-machine",
		Value:  "true",
		Effect: corev1.TaintEffectNoSchedule,
	}
	machine.Spec.Taints = []corev1.Taint{machineTaint}
	if err := tc.client.Update(context.TODO(), &machine); err != nil {
		return fmt.Errorf("error updating machine object with taint: %v", err)
	}
	glog.Info("Updated machine object with taint")
	var expectedTaints = sets.NewString("not-from-machine", "from-machine")
	err := wait.PollImmediate(1*time.Second, waitLong, func() (bool, error) {
		if err := tc.client.Get(context.TODO(), nodeKey, node); err != nil {
			glog.Errorf("error querying api for node object: %v", err)
			return false, nil
		}
		glog.Info("Got the node again for verification of taints")
		var observedTaints = sets.NewString()
		for _, taint := range node.Spec.Taints {
			observedTaints.Insert(taint.Key)
		}
		if expectedTaints.Difference(observedTaints).HasAny("not-from-machine", "from-machine") == false {
			glog.Infof("expected : %v, observed %v , difference %v, ", expectedTaints, observedTaints, expectedTaints.Difference(observedTaints))
			return true, nil
		}
		glog.Infof("All expected taints not found on node. Missing: %v", expectedTaints.Difference(observedTaints))
		return false, nil
	})
	return err
}

func (tc *testConfig) ExpectNewNodeWhenDeletingMachine() error {
	listOptions := client.ListOptions{
		Namespace: namespace,
	}
	machineList := capiv1alpha1.MachineList{}
	nodeList := corev1.NodeList{}

	glog.Info("Get machineList")
	err := wait.PollImmediate(1*time.Second, waitShort, func() (bool, error) {
		if err := tc.client.List(context.TODO(), &listOptions, &machineList); err != nil {
			glog.Errorf("error querying api for machineList object: %v, retrying...", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return err
	}

	glog.Info("Get nodeList")
	err = wait.PollImmediate(1*time.Second, waitShort, func() (bool, error) {
		if err := tc.client.List(context.TODO(), &listOptions, &nodeList); err != nil {
			glog.Errorf("error querying api for nodeList object: %v, retrying...", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return err
	}

	clusterInitialTotalNodes := len(nodeList.Items)
	clusterInitialTotalMachines := len(machineList.Items)
	var triagedWorkerMachine capiv1alpha1.Machine
	var triagedWorkerNode corev1.Node
MachineLoop:
	for _, m := range machineList.Items {
		if m.Labels["sigs.k8s.io/cluster-api-machine-role"] == "worker" {
			for _, n := range nodeList.Items {
				if m.Status.NodeRef == nil {
					glog.Errorf("no NodeRef found in machine %v", m.Name)
					return errors.New("no NodeRef found in machine")
				}
				if n.Name == m.Status.NodeRef.Name {
					triagedWorkerMachine = m
					triagedWorkerNode = n
					break MachineLoop
				}
			}
		}
	}

	glog.Info("Delete machine")
	err = wait.PollImmediate(1*time.Second, waitShort, func() (bool, error) {
		if err := tc.client.Delete(context.TODO(), &triagedWorkerMachine); err != nil {
			glog.Errorf("error querying api for Deployment object: %v, retrying...", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return err
	}

	err = wait.PollImmediate(1*time.Second, waitMedium, func() (bool, error) {
		if err := tc.client.List(context.TODO(), &listOptions, &machineList); err != nil {
			glog.Errorf("error querying api for machineList object: %v, retrying...", err)
			return false, nil
		}
		glog.Info("Expect new machine to come up")
		return len(machineList.Items) == clusterInitialTotalMachines, nil
	})
	if err != nil {
		return err
	}

	err = wait.PollImmediate(1*time.Second, waitLong, func() (bool, error) {
		if err := tc.client.List(context.TODO(), &listOptions, &nodeList); err != nil {
			glog.Errorf("error querying api for nodeList object: %v, retrying...", err)
			return false, nil
		}
		glog.Info("Expect deleted machine node to go away")
		for _, n := range nodeList.Items {
			if n.Name == triagedWorkerNode.Name {
				return false, nil
			}
		}
		glog.Info("Expect new node to come up")
		return len(nodeList.Items) == clusterInitialTotalNodes, nil
	})
	if err != nil {
		return err
	}
	return nil
}
