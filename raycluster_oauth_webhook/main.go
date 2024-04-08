package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/json"
	_ "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"net/http"
	"net/http/httputil"
	"sigs.k8s.io/yaml"

	rayv1api "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"strconv"
)

type ServerParameters struct {
	port     int
	certFile string
	keyFile  string
}

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

type Config struct {
	Containers []corev1.Container `yaml:"containers"`
	Volumes    []corev1.Volume    `yaml:"volumes"`
}

var (
	universalDeserializer = serializer.NewCodecFactory(runtime.NewScheme()).UniversalDeserializer()
	k8sConfig             *rest.Config
	k8sClientSet          *kubernetes.Clientset
	serverParameters      ServerParameters
)

func main() {
	flag.IntVar(&serverParameters.port, "port", 8080, "Webhook server port.")
	flag.StringVar(&serverParameters.certFile, "tlsCertFile", "/etc/webhook/certs/tls.crt", "File containing the x509 Certificate for HTTPS.")
	flag.StringVar(&serverParameters.keyFile, "tlsKeyFile", "/etc/webhook/certs/tls.key", "File containing the x509 private key to --tlsCertFile.")
	flag.Parse()

	// Creating Client Set.
	k8sClientSet = createClientSet()

	// test if client set is working
	http.HandleFunc("/", HandleRoot)
	http.HandleFunc("/mutate", HandleMutate)
	fmt.Print(http.ListenAndServeTLS(":"+strconv.Itoa(serverParameters.port), serverParameters.certFile, serverParameters.keyFile, nil))
}

func HandleRoot(w http.ResponseWriter, r *http.Request) {
	_, err := w.Write([]byte("HandleRoot!"))
	if err != nil {
		return
	}
}

func getAdmissionReviewRequest(w http.ResponseWriter, r *http.Request) admissionv1.AdmissionReview {
	requestDump, _ := httputil.DumpRequest(r, true)
	fmt.Printf("Request:\n%s\n", requestDump)

	// Grabbing the http body received on webhook.
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		fmt.Printf("Error reading webhook request: %v", err.Error())
	}

	// Required to pass to universal decoder.
	// v1beta1 also needs to be added to webhook.yaml
	var admissionReviewReq admissionv1.AdmissionReview

	fmt.Print("deserializing admission review request")
	if _, _, err := universalDeserializer.Decode(body, nil, &admissionReviewReq); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Printf("could not deserialize request: %v", err)
	} else if admissionReviewReq.Request == nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = errors.New("malformed admission review: request is nil")
	}

	return admissionReviewReq
}

func HandleMutate(w http.ResponseWriter, r *http.Request) {

	fmt.Print("beginning to handle mutate")
	admissionReviewReq := getAdmissionReviewRequest(w, r)

	var rayCluster rayv1api.RayCluster

	// Deserialize RayCluster from admission review request
	if err := json.Unmarshal(admissionReviewReq.Request.Object.Raw, &rayCluster); err != nil {
		fmt.Printf("could not unmarshal RayCluster from admission request: %v", err)
	}

	// Check if the request is for a RayCluster
	fmt.Print("looks like we got one: RayCluster")

	// Extract RayCluster and create patch
	patches, err := createPatch(&rayCluster)
	if err != nil {
		// Handle error
		fmt.Printf("error creating patch: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Serialize patches into JSON
	patchBytes, err := json.Marshal(patches)
	if err != nil {
		// Handle error
		fmt.Printf("error marshaling patches: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Log the original RayCluster YAML
	if err := json.Unmarshal(admissionReviewReq.Request.Object.Raw, &rayCluster); err != nil {
		fmt.Printf("error unmarshaling RayCluster: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	rayClusterYAML, err := yaml.Marshal(rayCluster)
	if err != nil {
		fmt.Printf("error marshaling RayCluster to YAML: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	fmt.Printf("Original RayCluster YAML:\n%s", string(rayClusterYAML))

	// Log the patches (reflecting the intended mutation)
	fmt.Printf("Generated patches: %s", string(patchBytes))

	// Add patchBytes to the admission response
	admissionReviewResponse := admissionv1.AdmissionReview{
		Response: &admissionv1.AdmissionResponse{
			UID:     admissionReviewReq.Request.UID,
			Allowed: true,
		},
	}
	admissionReviewResponse.Response.Patch = patchBytes

	// Submit the response
	bytes, err := json.Marshal(&admissionReviewResponse)
	if err != nil {
		fmt.Printf("marshaling response: %v", err)
	}

	_, err = w.Write(bytes)
	if err != nil {
		return
	}
}
