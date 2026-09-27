package apiserverresources

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/teutonet/cluster-api-provider-hosted-control-plane/api/v1alpha1"
	operatorutil "github.com/teutonet/cluster-api-provider-hosted-control-plane/pkg/operator/util"
	"github.com/teutonet/cluster-api-provider-hosted-control-plane/pkg/operator/util/names"
	"github.com/teutonet/cluster-api-provider-hosted-control-plane/pkg/util/tracing"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	capiv2 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// Keys of the service account keys secret. Only serviceAccountKeysBundleKey is
// mounted into the API server; the other two carry the state that makes the
// rotation idempotent.
const (
	serviceAccountCurrentKeyKey  = "current.pem"
	serviceAccountPreviousKeyKey = "previous.pem"
	serviceAccountKeysBundleKey  = "sa-keys.pub"
)

var errServiceAccountKeyMissing = errors.New("service account certificate has no public key yet")

// nextServiceAccountKeys computes the content of the service account keys
// secret from the current service account certificate and the secret as it
// is stored.
//
// cert-manager renews the service account certificate with a new private key,
// and the API server only verifies tokens against the keys it is given:
// without the previous key, every projected token issued before the renewal
// is rejected until the kubelets refresh it (up to ~50 min of Unauthorized
// for every in-cluster client). The API server therefore verifies against
// the current AND the previous public key, and signs with the current one.
//
// The previous key moves only when the current key actually changes, so a
// reconcile without a renewal leaves the secret untouched.
func nextServiceAccountKeys(currentCertificatePEM []byte, stored map[string][]byte) (map[string][]byte, error) {
	current, err := publicKeyPEMFromCertificate(currentCertificatePEM)
	if err != nil {
		return nil, err
	}
	storedCurrent := stored[serviceAccountCurrentKeyKey]
	previous := stored[serviceAccountPreviousKeyKey]
	if len(storedCurrent) > 0 && !bytes.Equal(storedCurrent, current) {
		previous = storedCurrent
	}

	data := map[string][]byte{
		serviceAccountCurrentKeyKey: current,
		serviceAccountKeysBundleKey: append(append([]byte{}, current...), previous...),
	}
	if len(previous) > 0 {
		data[serviceAccountPreviousKeyKey] = previous
	}
	return data, nil
}

// publicKeyPEMFromCertificate returns the public key of the first certificate
// in data, as a canonical PKIX "PUBLIC KEY" PEM block, so that two encodings
// of the same key compare equal.
func publicKeyPEMFromCertificate(data []byte) ([]byte, error) {
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, errServiceAccountKeyMissing
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse service account certificate: %w", err)
		}
		der, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal service account public key: %w", err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
	}
}

// reconcileServiceAccountKeys writes the service account keys secret. It runs
// before the API server deployment in the same reconcile: the deployment's
// secret checksum then covers the new signing key and the extended key bundle
// together, so a single rollout brings both. Returns false while the service
// account certificate has not been issued yet.
func (arr *apiServerResourcesReconciler) reconcileServiceAccountKeys(
	ctx context.Context,
	hostedControlPlane *v1alpha1.HostedControlPlane,
	cluster *capiv2.Cluster,
) (bool, error) {
	return tracing.WithSpan(ctx, arr.Tracer, "ReconcileServiceAccountKeys",
		func(ctx context.Context, span trace.Span) (bool, error) {
			secrets := arr.ManagementClusterClient.CoreV1().Secrets(hostedControlPlane.Namespace)
			serviceAccountSecret, err := secrets.Get(ctx, names.GetServiceAccountSecretName(cluster), metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			} else if err != nil {
				return false, fmt.Errorf("failed to get service account secret: %w", err)
			}

			var stored map[string][]byte
			if keysSecret, err := secrets.Get(
				ctx, names.GetServiceAccountKeysSecretName(cluster), metav1.GetOptions{},
			); err == nil {
				stored = keysSecret.Data
			} else if !apierrors.IsNotFound(err) {
				return false, fmt.Errorf("failed to get service account keys secret: %w", err)
			}

			data, err := nextServiceAccountKeys(serviceAccountSecret.Data[corev1.TLSCertKey], stored)
			if errors.Is(err, errServiceAccountKeyMissing) {
				return false, nil
			} else if err != nil {
				return false, err
			}

			return true, arr.ReconcileSecret(
				ctx,
				operatorutil.GetOwnerReferenceApplyConfiguration(hostedControlPlane),
				names.GetControlPlaneLabels(cluster, "service-account-keys"),
				hostedControlPlane.Namespace,
				names.GetServiceAccountKeysSecretName(cluster),
				false,
				data,
				corev1.SecretTypeOpaque,
			)
		},
	)
}
