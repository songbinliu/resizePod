package main

import (
	"flag"
	"fmt"
	"github.com/golang/glog"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/api/v1"
)

//global variables
var (
	masterUrl            string
	kubeConfig           string
	nameSpace            string
	podName              string
	noexistSchedulerName string
	nodeName             string
	k8sVersion           string
	memLimit             int
	cpuLimit             int
)

const (
	// a non-exist scheduler: make sure the pods won't be scheduled by default-scheduler during our moving
	DefaultNoneExistSchedulerName = "turbo-none-exist-scheduler"
	KindReplicationController     = "ReplicationController"
	KindReplicaSet                = "ReplicaSet"
)

func setFlags() {
	flag.StringVar(&masterUrl, "masterUrl", "", "master url")
	flag.StringVar(&kubeConfig, "kubeConfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&nameSpace, "nameSpace", "default", "kubernetes object namespace")
	flag.StringVar(&podName, "podName", "", "the podName to be handled")
	flag.StringVar(&noexistSchedulerName, "scheduler-name", DefaultNoneExistSchedulerName, "the name of the none-exist-scheduler")
	flag.StringVar(&nodeName, "nodeName", "", "Destination of move")
	flag.StringVar(&k8sVersion, "k8sVersion", "1.6", "the version of Kubenetes cluster, candidates are 1.5 | 1.6")
	flag.IntVar(&memLimit, "memLimit", 0, "the memory limit in MB. 0 means no change")
	flag.IntVar(&cpuLimit, "cpuLimit", 0, "the cpu limit in m. 0 means no change")

	flag.Set("alsologtostderr", "true")
	flag.Parse()

	fmt.Printf("kubeConfig=%s, cpu=%d, pod=%s\n", kubeConfig, cpuLimit, podName)
}

func addErrors(prefix string, err1, err2 error) error {
	rerr := fmt.Errorf("%v ", prefix)
	if err1 != nil {
		rerr = fmt.Errorf("%v %v", rerr.Error(), err1.Error())
	}

	if err2 != nil {
		rerr = fmt.Errorf("%v %v", rerr.Error(), err2.Error())
	}

	glog.Errorf("check update faild:%v", rerr.Error())
	return rerr
}

// update the parent's scheduler before moving pod; then restore parent's scheduler
func doSchedulerResize(client *kclient.Clientset, pod *v1.Pod, parentKind, parentName string, req v1.ResourceList) error {
	id := fmt.Sprintf("%v/%v", pod.Namespace, pod.Name)
	//2. update the schedulerName
	var update func(*kclient.Clientset, string, string, string) (string, error)
	switch parentKind {
	case KindReplicationController:
		glog.V(3).Infof("pod-%v parent is a ReplicationController-%v", id, parentName)
		update = updateRCscheduler
	case KindReplicaSet:
		glog.V(2).Infof("pod-%v parent is a ReplicaSet-%v", id, parentName)
		update = updateRSscheduler
	default:
		err := fmt.Errorf("unsupported parent-[%v] Kind-[%v]", parentName, parentKind)
		glog.Warning(err.Error())
		return err
	}

	noexist := noexistSchedulerName
	check := checkSchedulerName
	nameSpace := pod.Namespace

	preScheduler, err := update(client, nameSpace, parentName, noexist)
	if flag, err2 := check(client, parentKind, nameSpace, parentName, noexist); !flag {
		prefix := fmt.Sprintf("move-failed: pod-[%v], parent-[%v]", id, parentName)
		return addErrors(prefix, err, err2)
	}

	restore := func() {
		//check it again in case somebody has changed it back.
		if flag, _ := check(client, parentKind, nameSpace, parentName, noexist); flag {
			update(client, nameSpace, parentName, preScheduler)
		}
	}
	defer restore()

	//3. movePod
	return resizePod(client, pod, req)
}

func ResizePod(client *kclient.Clientset, nameSpace, podName string, req v1.ResourceList) error {
	podClient := client.CoreV1().Pods(nameSpace)
	id := fmt.Sprintf("%v/%v", nameSpace, podName)

	//1. get original Pod
	getOption := metav1.GetOptions{}
	pod, err := podClient.Get(podName, getOption)
	if err != nil {
		err = fmt.Errorf("move-aborted: get original pod:%v\n%v", id, err.Error())
		glog.Error(err.Error())
		return err
	}

	//2. invalidate the schedulerName of parent controller
	parentKind, parentName, err := getParentInfo(pod)
	if err != nil {
		return fmt.Errorf("move-abort: cannot get pod-%v parent info: %v", id, err.Error())
	}

	//2.1 if pod is barely standalone pod, move it directly
	if parentKind == "" {
		return resizePod(client, pod, req)
	}

	//2.2 if pod controlled by ReplicationController/ReplicaSet, then need to do more
	return doSchedulerResize(client, pod, parentKind, parentName, req)

	//if k8sVersion == "1.5" {
	//	return doSchedulerMove15(client, pod, parentKind, parentName, req)
	//} else {
	//	return doSchedulerMove(client, pod, parentKind, parentName, req)
	//}

	return nil
}

func parseInputLimit() (v1.ResourceList, error) {
	if cpuLimit <= 0 && memLimit <= 0 {
		err := fmt.Errorf("cpuLimit=[%d], memLimit=[%d]", cpuLimit, memLimit)
		glog.Error(err)
		return nil, err
	}

	result := make(v1.ResourceList)
	if cpuLimit > 0 {
		result[v1.ResourceCPU] = resource.MustParse(fmt.Sprintf("%dm", cpuLimit))
	}
	if memLimit > 0 {
		result[v1.ResourceMemory] = resource.MustParse(fmt.Sprintf("%dMi", memLimit))
	}

	if cpu, exist := result[v1.ResourceCPU]; exist {
		glog.V(2).Infof("cpu: %+v, %v", cpu, cpu.MilliValue())
	}
	if mem, exist := result[v1.ResourceMemory]; exist {
		glog.V(2).Infof("memory: %+v, %v", mem, mem.Value())
	}
	return result, nil
}

func testResize(client *kclient.Clientset) {
	request, err := parseInputLimit()
	if err != nil {
		return
	}

	if err := ResizePod(client, nameSpace, podName, request); err != nil {
		glog.Errorf("move pod failed: %v/%v, %v", nameSpace, podName, err.Error())
		return
	}

	glog.V(2).Infof("sleep 10 seconds to check the final state")
	time.Sleep(time.Second * 10)
	if err := checkPodHealth(client, nameSpace, podName); err != nil {
		glog.Errorf("move pod failed: %v", err.Error())
		return
	}

	glog.V(2).Infof("resize pod(%v/%v) successfully", nameSpace, podName)
}

func main() {
	setFlags()
	defer glog.Flush()


	kubeClient := getKubeClient(&masterUrl, &kubeConfig)
	if kubeClient == nil {
		glog.Errorf("failed to get a k8s client for masterUrl=[%v], kubeConfig=[%v]", masterUrl, kubeConfig)
		return
	}

	if podName == "" {
		glog.Errorf("nodeName should not be empty.")
		return
	}

	parseInputLimit()

	PrintPodResource(kubeClient, nameSpace, podName)
	testResize(kubeClient)
	PrintPodResource(kubeClient, nameSpace, podName)
	return
}
