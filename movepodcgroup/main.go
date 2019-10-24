package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	podName := os.Args[1]
	targetCgroup := ""
	if len(os.Args) > 2 {
		targetCgroup = os.Args[2]
	}

	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	pods, err := clientset.CoreV1().Pods("default").List(metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	var thePod *corev1.Pod
	for _, pod := range pods.Items {
		//fmt.Printf("%s:%s %v\n", pod.ObjectMeta.Namespace, pod.ObjectMeta.Name, pod.ObjectMeta.Labels)
		if pod.ObjectMeta.Labels["qos"] == "dynamic" {
			fmt.Printf("%s %s %s\n", pod.Spec.NodeName, pod.ObjectMeta.UID, pod.ObjectMeta.Name)
			if pod.ObjectMeta.Name == podName {
				thePod = &pod
				break
			}
		}
	}

	if thePod == nil {
		fmt.Printf("Pod %s not found\n", podName)
		return
	}

	podCgroupPath, err := findBurstablePodCgroup(thePod)
	if err != nil {
		panic(err)
	}
	if podCgroupPath == "" {
		fmt.Printf("Failed to find cgroup path for pod %s\n", thePod.ObjectMeta.Name)
		return
	}
	fmt.Printf("Cgroup path for pod %s: %s\n", thePod.ObjectMeta.Name, podCgroupPath)

	containersCgroups, err := findContainersCgroups(podCgroupPath)
	if err != nil {
		fmt.Printf("Failed to get containers groups in %s: %v", podCgroupPath, err)
		return
	}

	for _, ccgroup := range containersCgroups {
		targetCCgroup := filepath.Join("/sys/fs/cgroup/cpu/kubepods/burstable", targetCgroup, filepath.Base(ccgroup))
		if err := moveCpuCgroup(ccgroup, targetCCgroup); err != nil {
			fmt.Printf("Failed to move cgroup %s to %s: %v", ccgroup, targetCCgroup, err)
			return
		}
	}

	// targetCgroupPath := filepath.Join("/sys/fs/cgroup/cpu/kubepods/burstable", targetCgroup, fmt.Sprintf("pod%s", thePod.ObjectMeta.UID))
	// if podCgroupPath == targetCgroupPath {
	// 	fmt.Printf("Pod %s is already in cgroup %s\n", thePod.ObjectMeta.Name, podCgroupPath)
	// 	return
	// }

	// fmt.Printf("Moving pod %s to cgroup %s\n", thePod.ObjectMeta.Name, targetCgroupPath)
	// if err := moveCpuCgroup(podCgroupPath, targetCgroupPath); err != nil {
	// 	fmt.Printf("Failed to move pod %s into cgroup %s: %v\n", thePod.ObjectMeta.Name, targetCgroupPath, err)
	// 	return
	// }

	fmt.Println("Success")

}

func findBurstablePodCgroup(pod *corev1.Pod) (string, error) {
	burstableCgroupPath := "/sys/fs/cgroup/cpu/kubepods/burstable"
	var podCgroupPath string
	e := filepath.Walk(burstableCgroupPath, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == fmt.Sprintf("pod%s", pod.ObjectMeta.UID) && info.IsDir() {
			podCgroupPath = path
		}
		return nil
	})

	if e != nil {
		return "", e
	}

	return podCgroupPath, e
}

func moveCpuCgroup(sourcePath, targetPath string) error {
	fmt.Printf("moveCpuCgroup(%s, %s)\n\n", sourcePath, targetPath)
	if err := os.MkdirAll(targetPath, 0755); err != nil {
		return fmt.Errorf("Failed to create target cgroup path: %v", err)
	}
	e := filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
		if err == nil && path != sourcePath && info.IsDir() {
			return moveCpuCgroup(path, filepath.Join(targetPath, info.Name()))
		}
		return nil
	})
	if e != nil {
		return fmt.Errorf("Failed to move child cgroup directories: %v", e)
	}

	filesToCopy := []string{"cpu.cfs_period_us", "cpu.cfs_quota_us", "cpu.rt_period_us", "cpu.rt_runtime_us", "cpu.shares"}
	for _, f := range filesToCopy {
		if err := catInto(filepath.Join(sourcePath, f), filepath.Join(targetPath, f)); err != nil {
			return fmt.Errorf("Failed to copy %s: %v", f, err)
		}
	}

	if err := moveTasks(sourcePath, targetPath); err != nil {
		return fmt.Errorf("Failed to move processes to target cgroup: %v", err)
	}

	// if err := os.Remove(sourcePath); err != nil {
	// 	return fmt.Errorf("Failed to delete cgroup %s: %v", sourcePath, err)
	// }

	return nil
}

func catInto(sourceFile, targetFile string) error {
	sourceContent, err := ioutil.ReadFile(sourceFile)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(targetFile, sourceContent, 0644)
}

func moveTasks(sourceCgroup, targetCgroup string) error {
	for {
		content, err := ioutil.ReadFile(filepath.Join(sourceCgroup, "tasks"))
		if err != nil {
			return err
		}

		procs := strings.Split(strings.TrimSpace(string(content)), "\n")

		didOne := false

		for _, p := range procs {
			if strings.TrimSpace(p) == "" {
				continue
			}
			didOne = true
			if err := ioutil.WriteFile(filepath.Join(targetCgroup, "tasks"), []byte(p), 0644); err != nil {
				fmt.Printf("Failed to write %s to target tasks: %v", p, err)
			}
		}
		if !didOne {
			return nil
		}
	}
}

func findContainersCgroups(podCgroupPath string) ([]string, error) {
	fileInfos, err := ioutil.ReadDir(podCgroupPath)
	if err != nil {
		return nil, err
	}

	result := []string{}
	for _, info := range fileInfos {
		if !info.IsDir() {
			continue
		}
		result = append(result, filepath.Join(podCgroupPath, info.Name()))
	}

	return result, nil
}
