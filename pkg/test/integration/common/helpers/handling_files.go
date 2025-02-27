package helpers

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	v1alpha1 "github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	mcmscheme "github.com/gardener/machine-controller-manager/pkg/client/clientset/versioned/scheme"
	v1 "k8s.io/api/apps/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apiextensionsscheme "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/scheme"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	scheme "k8s.io/client-go/kubernetes/scheme"
)

// parsek8sYaml reads a yaml file and parses it based on the scheme
func parseK8sYaml(filepath string) ([]runtime.Object, []*schema.GroupVersionKind, error) {
	fileR, err := ioutil.ReadFile(filepath)
	if err != nil {
		return nil, nil, err
	}

	acceptedK8sTypes := regexp.MustCompile(`(Role|ClusterRole|RoleBinding|ClusterRoleBinding|ServiceAccount|CustomResourceDefinition|kind: Deployment)`)
	acceptedMCMTypes := regexp.MustCompile(`(MachineClass|Machine)`)
	fileAsString := string(fileR[:])
	sepYamlfiles := strings.Split(fileAsString, "---")
	retObj := make([]runtime.Object, 0, len(sepYamlfiles))
	retKind := make([]*schema.GroupVersionKind, 0, len(sepYamlfiles))
	var retErr error
	crdRegexp, _ := regexp.Compile("CustomResourceDefinition")
	for _, f := range sepYamlfiles {
		if f == "\n" || f == "" {
			// ignore empty cases
			continue
		}

		// isExist, err := regexp.Match("CustomResourceDefinition", []byte(f))
		if crdRegexp.Match([]byte(f)) {
			decode := apiextensionsscheme.Codecs.UniversalDeserializer().Decode
			obj, groupVersionKind, err := decode([]byte(f), nil, nil)
			if err != nil {
				log.Println(fmt.Sprintf("Error while decoding YAML object. Err was: %s", err))
				retErr = err
				continue
			}
			if !acceptedK8sTypes.MatchString(groupVersionKind.Kind) {
				log.Printf("The custom-roles configMap contained K8s object types which are not supported! Skipping object with type: %s", groupVersionKind.Kind)
			} else {
				retKind = append(retKind, groupVersionKind)
				retObj = append(retObj, obj)
			}
		} else if acceptedK8sTypes.Match([]byte(f)) {
			decode := scheme.Codecs.UniversalDeserializer().Decode
			obj, groupVersionKind, err := decode([]byte(f), nil, nil)
			if err != nil {
				log.Println(fmt.Sprintf("Error while decoding YAML object. Err was: %s", err))
				retErr = err
				continue
			}
			retKind = append(retKind, groupVersionKind)
			retObj = append(retObj, obj)

		} else {
			decode := mcmscheme.Codecs.UniversalDeserializer().Decode
			obj, groupVersionKind, err := decode([]byte(f), nil, nil)
			if err != nil {
				log.Println(fmt.Sprintf("Error while decoding YAML object. Err was: %s", err))
				retErr = err
				continue
			}
			if !acceptedMCMTypes.MatchString(groupVersionKind.Kind) {
				log.Printf("The custom-roles configMap contained K8s object types which are not supported! Skipping object with type: %s", groupVersionKind.Kind)
			} else {
				retKind = append(retKind, groupVersionKind)
				retObj = append(retObj, obj)
			}
		}
	}
	return retObj, retKind, retErr
}

// applyFile uses yaml to create resources in kubernetes
func (c *Cluster) ApplyFile(filePath string, namespace string) error {
	/* TO-DO: This function checks for the availability of filePath
	if available, then apply that file to kubernetes cluster c
	*/
	runtimeobj, kind, err := parseK8sYaml(filePath)
	if err == nil {
		for key, obj := range runtimeobj {
			switch kind[key].Kind {
			case "CustomResourceDefinition":
				crdName := obj.(*apiextensionsv1beta1.CustomResourceDefinition).Name
				crd := obj.(*apiextensionsv1beta1.CustomResourceDefinition)
				_, err := c.apiextensionsClient.ApiextensionsV1beta1().CustomResourceDefinitions().Create(crd)
				if err != nil {
					if strings.Contains(err.Error(), "already exists") {
					} else {
						return err
					}
				}
				err = c.checkEstablished(crdName)
				if err != nil {
					log.Printf("%s crd can not be established because of an error\n", crdName)
					return err
				}
			case "MachineClass":
				resource := obj.(*v1alpha1.MachineClass)
				_, err := c.McmClient.MachineV1alpha1().MachineClasses(namespace).Create(resource)
				if err != nil {
					return err
				}
			case "Machine":
				resource := obj.(*v1alpha1.Machine)
				_, err := c.McmClient.MachineV1alpha1().Machines(namespace).Create(resource)
				if err != nil {
					return err
				}
			case "MachineDeployment":
				resource := obj.(*v1alpha1.MachineDeployment)
				_, err := c.McmClient.MachineV1alpha1().MachineDeployments(namespace).Create(resource)
				if err != nil {
					return err
				}
			case "Deployment":
				resource := obj.(*v1.Deployment)
				_, err := c.Clientset.AppsV1().Deployments(namespace).Create(resource)
				if err != nil {
					return err
				}
			}
		}
	} else {
		return err
	}
	return nil
}

// checkEstablished uses the specified name to check if it is established
func (c *Cluster) checkEstablished(crdName string) error {
	/* This function checks the CRD for an established status
	if established status is not met, it will sleep until it meets
	*/
	err := wait.Poll(500*time.Millisecond, 60*time.Second, func() (bool, error) {
		crd, err := c.apiextensionsClient.ApiextensionsV1beta1().CustomResourceDefinitions().Get(crdName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, cond := range crd.Status.Conditions {
			switch cond.Type {
			case apiextensionsv1beta1.Established:
				if cond.Status == apiextensionsv1beta1.ConditionTrue {
					// log.Printf("crd %s is established/ready\n", crdName)
					return true, err
				}
			case apiextensionsv1beta1.NamesAccepted:
				if cond.Status == apiextensionsv1beta1.ConditionFalse {
					log.Printf("Name conflict: %v\n", cond.Reason)
					log.Printf("Naming Conflict with created CRD %s\n", crdName)
				}
			}
		}
		return false, err
	})
	return err
}

func (c *Cluster) ApplyFiles(source string, namespace string) error {
	var files []string
	err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		files = append(files, path)
		return nil
	})
	if err != nil {
		panic(err)
	}

	for _, file := range files {
		// log.Println(file)
		fi, err := os.Stat(file)
		if err != nil {
			log.Println("Error: file does not exist!")
			return err
		}

		switch mode := fi.Mode(); {
		case mode.IsDir():
			// do directory stuff
			log.Printf("%s is a directory.\n", file)
		case mode.IsRegular():
			// do file stuff
			err := c.ApplyFile(file, namespace)
			if err != nil {
				//Ignore error if it says the crd already exists
				if !strings.Contains(err.Error(), "already exists") {
					log.Printf("Failed to apply yaml file %s", file)
					//return err
				}
			}
			log.Printf("file %s has been successfully applied to cluster", file)
		}
	}
	return nil
}

/*
RotateLogFile takes file name as input and returns a file object obtained by os.Create
If the file exists already then it renames it so that a new file can be created
*/
func RotateLogFile(fileName string) (*os.File, error) {
	/*
	  startMachineController starts the machine controller
	*/
	if _, err := os.Stat(fileName); err == nil { // !strings.Contains(err.Error(), "no such file or directory") {
		for i := 9; i > 0; i-- {
			os.Rename(fmt.Sprintf("%s.%d", fileName, i), fmt.Sprintf("%s.%d", fileName, i+1))
		}
		os.Rename(fileName, fmt.Sprintf("%s.%d", fileName, 1))
	}
	fileObj, err := os.Create(fileName)
	return fileObj, err
}
