package apiserverresources

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"github.com/teutonet/cluster-api-provider-hosted-control-plane/api/v1alpha1"
	"github.com/teutonet/cluster-api-provider-hosted-control-plane/pkg/operator/util/names"
	"github.com/teutonet/cluster-api-provider-hosted-control-plane/pkg/reconcilers"
	"github.com/teutonet/cluster-api-provider-hosted-control-plane/pkg/reconcilers/alias"
	"github.com/teutonet/cluster-api-provider-hosted-control-plane/pkg/util/tracing"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/util/keyutil"
	konstants "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	capiv2 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// serviceAccountKey is what cert-manager writes into <cluster>-service-account:
// a certificate for the key, and the key itself.
type serviceAccountKey struct {
	key            *rsa.PrivateKey
	certificatePEM []byte
}

func newServiceAccountKey(t *testing.T) serviceAccountKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "service-account"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return serviceAccountKey{
		key:            key,
		certificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// verifies reports whether the API server, given bundle as
// --service-account-key-file, accepts a signature made with key. It parses the
// bundle with keyutil.ParsePublicKeysPEM, as kube-apiserver does.
func verifies(t *testing.T, bundle []byte, key *rsa.PrivateKey) bool {
	t.Helper()
	keys, err := keyutil.ParsePublicKeysPEM(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("token"))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	for _, publicKey := range keys {
		if rsaKey, ok := publicKey.(*rsa.PublicKey); ok &&
			rsa.VerifyPKCS1v15(rsaKey, crypto.SHA256, digest[:], signature) == nil {
			return true
		}
	}
	return false
}

func TestNextServiceAccountKeys_KeepsThePreviousKeyAcrossARotation(t *testing.T) {
	g := NewWithT(t)
	a, b, c := newServiceAccountKey(t), newServiceAccountKey(t), newServiceAccountKey(t)

	// First reconcile: nothing stored yet, only the current key.
	first, err := nextServiceAccountKeys(a.certificatePEM, nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(first).NotTo(HaveKey(serviceAccountPreviousKeyKey))
	g.Expect(verifies(t, first[serviceAccountKeysBundleKey], a.key)).To(BeTrue())

	// Reconcile without a renewal: nothing moves.
	again, err := nextServiceAccountKeys(a.certificatePEM, first)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(again).To(Equal(first))

	// Renewal with a new key: tokens signed with either key are accepted.
	rotated, err := nextServiceAccountKeys(b.certificatePEM, again)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(rotated[serviceAccountPreviousKeyKey]).To(Equal(first[serviceAccountCurrentKeyKey]))
	g.Expect(verifies(t, rotated[serviceAccountKeysBundleKey], b.key)).To(BeTrue())
	g.Expect(verifies(t, rotated[serviceAccountKeysBundleKey], a.key)).To(BeTrue(),
		"a token issued before the rotation must still be accepted")

	// Reconcile again after the renewal: the previous key must NOT be replaced
	// by the current one, or the old tokens would be rejected on the next pass.
	stable, err := nextServiceAccountKeys(b.certificatePEM, rotated)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stable).To(Equal(rotated))
	g.Expect(verifies(t, stable[serviceAccountKeysBundleKey], a.key)).To(BeTrue())

	// Next renewal: only one generation back is kept.
	next, err := nextServiceAccountKeys(c.certificatePEM, stable)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(verifies(t, next[serviceAccountKeysBundleKey], c.key)).To(BeTrue())
	g.Expect(verifies(t, next[serviceAccountKeysBundleKey], b.key)).To(BeTrue())
	g.Expect(verifies(t, next[serviceAccountKeysBundleKey], a.key)).To(BeFalse())
}

func TestNextServiceAccountKeys_WaitsForTheCertificate(t *testing.T) {
	g := NewWithT(t)
	_, err := nextServiceAccountKeys(nil, nil)
	g.Expect(err).To(MatchError(errServiceAccountKeyMissing))
}

func TestReconcileServiceAccountKeys_AgainstTheAPI(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	a, b := newServiceAccountKey(t), newServiceAccountKey(t)
	cluster := &capiv2.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
	hostedControlPlane := &v1alpha1.HostedControlPlane{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns", UID: "uid"},
	}
	client := fake.NewClientset()
	reconciler := &apiServerResourcesReconciler{
		ManagementResourceReconciler: reconcilers.ManagementResourceReconciler{
			ManagementClusterClient: &alias.ManagementClusterClient{Interface: client},
			Tracer:                  tracing.GetTracer("test"),
		},
	}
	secrets := client.CoreV1().Secrets("ns")

	// No service account certificate yet: not ready, nothing written.
	ready, err := reconciler.reconcileServiceAccountKeys(ctx, hostedControlPlane, cluster)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ready).To(BeFalse())

	serviceAccountSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: names.GetServiceAccountSecretName(cluster), Namespace: "ns"},
		Data:       map[string][]byte{corev1.TLSCertKey: a.certificatePEM},
	}
	_, err = secrets.Create(ctx, serviceAccountSecret, metav1.CreateOptions{})
	g.Expect(err).NotTo(HaveOccurred())

	readBundle := func() []byte {
		keys, err := secrets.Get(ctx, names.GetServiceAccountKeysSecretName(cluster), metav1.GetOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		return keys.Data[serviceAccountKeysBundleKey]
	}

	ready, err = reconciler.reconcileServiceAccountKeys(ctx, hostedControlPlane, cluster)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ready).To(BeTrue())
	g.Expect(verifies(t, readBundle(), a.key)).To(BeTrue())

	// cert-manager renews the certificate with a new key.
	serviceAccountSecret.Data[corev1.TLSCertKey] = b.certificatePEM
	_, err = secrets.Update(ctx, serviceAccountSecret, metav1.UpdateOptions{})
	g.Expect(err).NotTo(HaveOccurred())

	for range 2 { // the second pass must not drop the previous key
		_, err = reconciler.reconcileServiceAccountKeys(ctx, hostedControlPlane, cluster)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(verifies(t, readBundle(), b.key)).To(BeTrue())
		g.Expect(verifies(t, readBundle(), a.key)).To(BeTrue())
	}
}

func TestAPIServerCertificatesVolume_VerifiesWithTheKeyBundle(t *testing.T) {
	g := NewWithT(t)
	cluster := &capiv2.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
	volume := (&apiServerResourcesReconciler{}).createAPIServerCertificatesVolume(cluster)

	var publicKeyFrom, privateKeyFrom []string
	for _, source := range volume.Projected.Sources {
		if source.Secret == nil {
			continue
		}
		for _, item := range source.Secret.Items {
			switch *item.Path {
			case konstants.ServiceAccountPublicKeyName:
				publicKeyFrom = append(publicKeyFrom, *source.Secret.Name+"/"+*item.Key)
			case konstants.ServiceAccountPrivateKeyName:
				privateKeyFrom = append(privateKeyFrom, *source.Secret.Name+"/"+*item.Key)
			}
		}
	}
	g.Expect(publicKeyFrom).To(Equal([]string{
		names.GetServiceAccountKeysSecretName(cluster) + "/" + serviceAccountKeysBundleKey,
	}))
	g.Expect(privateKeyFrom).To(Equal([]string{
		names.GetServiceAccountSecretName(cluster) + "/" + corev1.TLSPrivateKeyKey,
	}))
}
