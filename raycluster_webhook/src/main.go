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
	"crypto/tls"
	"crypto/x509"
)

// ServerParameters : we need to enable a TLS endpoint
// Let's take some parameters where we can set the path to the TLS certificate and port number to run on.
type ServerParameters struct {
	port           int    // webhook server port
	certFile       string // path to the x509 certificate for https
	keyFile        string // path to the x509 private key matching `CertFile`
}

// To pfform a simple mutation on the object before the Kubernetes API sees the object, we can apply a patch to the operation. RFC6902
type patchOperation struct {
	Op    string      `json:"op"`  // Operation
	Path  string      `json:"path"` // Path
	Value interface{} `json:"value,omitempty"`
}

// Config To perform patching to Pod definition
type Config struct {
	Containers []corev1.Container `yaml:"containers"`
	Volumes    []corev1.Volume    `yaml:"volumes"`
}

var (
	universalDeserializer = serializer.NewCodecFactory(runtime.NewScheme()).UniversalDeserializer()
	k8sConfig *rest.Config
	k8sClientSet *kubernetes.Clientset
	serverParameters ServerParameters
)


func main() {
    // Parse command line flags to get the paths to TLS certificate and private key
    var serverParameters struct {
        port     int
        certFile string
        keyFile  string
    }
    flag.IntVar(&serverParameters.port, "port", 8443, "Webhook server port.")
    flag.StringVar(&serverParameters.certFile, "tlsCertFile", "/etc/webhook/certs/tls.crt", "File containing the x509 Certificate for HTTPS.")
    flag.StringVar(&serverParameters.keyFile, "tlsKeyFile", "/etc/webhook/certs/tls.key", "File containing the x509 private key to --tlsCertFile.")
    flag.Parse()

    // Load CA certificate
    caCert, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/service-ca.crt")
    if err != nil {
        fmt.Println("Failed to load CA certificate:", err)
        return
    }

    caCertPool := x509.NewCertPool()
    caCertPool.AppendCertsFromPEM(caCert)

    // Load server's certificate and key
    cert, err := tls.LoadX509KeyPair(serverParameters.certFile, serverParameters.keyFile)
    if err != nil {
        fmt.Println("Failed to load server certificate and key:", err)
        return
    }

    // Create a TLS config that uses the server's cert/key and also trusts the CA
    tlsConfig := &tls.Config{
        Certificates: []tls.Certificate{cert},
        ClientCAs:    caCertPool,
        ClientAuth:   tls.RequireAndVerifyClientCert,
    }
    tlsConfig.BuildNameToCertificate()

    // Configure HTTPS server
    server := &http.Server{
        Addr:      ":" + strconv.Itoa(serverParameters.port),
        TLSConfig: tlsConfig,
    }

    http.HandleFunc("/", HandleRoot)
    http.HandleFunc("/mutate", HandleMutate)

    // Start HTTPS server with TLS config
    fmt.Println("Starting server on port", serverParameters.port)
    err = server.ListenAndServeTLS("", "") // Certificates are already loaded in the TLSConfig
    if err != nil {
        fmt.Println("Failed to start server:", err)
    }
}

func HandleRoot(w http.ResponseWriter, r *http.Request){
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
		fmt.Errorf("Error reading webhook request: ", err.Error())
	}

	// Required to pass to universal decoder.
	// v1beta1 also needs to be added to webhook.yaml
	var admissionReviewReq admissionv1.AdmissionReview

	fmt.Print("deserializing admission review request")
	if _, _, err := universalDeserializer.Decode(body, nil, &admissionReviewReq); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Errorf("could not deserialize request: %v", err)
	} else if admissionReviewReq.Request == nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = errors.New("malformed admission review: request is nil")
	}
	
	return admissionReviewReq
}


func HandleMutate(w http.ResponseWriter, r *http.Request){

    fmt.Print("beginning to handle mutate")
    admissionReviewReq := getAdmissionReviewRequest(w, r)

	 var rayCluster rayv1api.RayCluster

    // Deserialize RayCluster from admission review request
    if err := json.Unmarshal(admissionReviewReq.Request.Object.Raw, &rayCluster); err != nil {
         fmt.Errorf("could not unmarshal RayCluster from admission request: %v", err)
    }


    // Check if the request is for a RayCluster
        fmt.Print("looks like we got one: RayCluster")

        // Extract RayCluster and create patch
        patches, err := createPatch(&rayCluster)
        if err != nil {
            // Handle error
            fmt.Errorf("error creating patch: %v", err)
            w.WriteHeader(http.StatusInternalServerError)
            return
        }

        // Serialize patches into JSON
        patchBytes, err := json.Marshal(patches)
        if err != nil {
            // Handle error
            fmt.Errorf("error marshaling patches: %v", err)
            w.WriteHeader(http.StatusInternalServerError)
            return
        }

        // Log the original RayCluster YAML
        if err := json.Unmarshal(admissionReviewReq.Request.Object.Raw, &rayCluster); err != nil {
            fmt.Errorf("error unmarshaling RayCluster: %v", err)
            w.WriteHeader(http.StatusInternalServerError)
            return
        }
        rayClusterYAML, err := yaml.Marshal(rayCluster)
        if err != nil {
            fmt.Errorf("error marshaling RayCluster to YAML: %v", err)
            w.WriteHeader(http.StatusInternalServerError)
            return
        }
        fmt.Printf("Original RayCluster YAML:\n%s", string(rayClusterYAML))

        // Log the patches (reflecting the intended mutation)
        fmt.Printf("Generated patches: %s", string(patchBytes))

	// Add patchBytes to the admission response
	admissionReviewResponse := admissionv1.AdmissionReview{
		Response: &admissionv1.AdmissionResponse{
			UID: admissionReviewReq.Request.UID,
			Allowed: true,
		},
	}
	admissionReviewResponse.Response.Patch = patchBytes

	// Submit the response
	bytes, err := json.Marshal(&admissionReviewResponse)
	if err != nil {
		fmt.Errorf("marshaling response: %v", err)
	}

	_, err = w.Write(bytes)
	if err != nil {
		return
	}
}

