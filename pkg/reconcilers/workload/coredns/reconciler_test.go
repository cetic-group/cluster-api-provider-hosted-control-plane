package coredns

import (
	"bytes"
	"testing"

	"github.com/coredns/caddy/caddyfile"
	. "github.com/onsi/gomega"
	"github.com/teutonet/cluster-api-provider-hosted-control-plane/pkg/reconcilers/alias"
	. "github.com/teutonet/cluster-api-provider-hosted-control-plane/test"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const expectedCorefile = `.:53 {
    errors
    health {
        lameduck 5s
    }
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa {
        fallthrough in-addr.arpa ip6.arpa
        ttl 30
    }
    prometheus :9153
    forward . /etc/resolv.conf {
        max_concurrent 1000
    }
    cache 30 {
        disable success cluster.local
        disable denial cluster.local
    }
    loop
    loadbalance
    reload
    import /etc/coredns/custom/*.override
}
import /etc/coredns/custom/*.server
`

func newTestCoreDNSReconciler(client *fake.Clientset) *coreDNSReconciler {
	//nolint:forcetypeassert // the constructor always returns a *coreDNSReconciler
	return NewCoreDNSReconciler(
		&alias.WorkloadClusterClient{Interface: client}, nil, "cluster.local", nil,
	).(*coreDNSReconciler)
}

func TestBuildCorefile(t *testing.T) {
	g, _, _ := G(t)
	reconciler := newTestCoreDNSReconciler(fake.NewClientset())

	corefile := reconciler.buildCorefile()
	g.Expect(corefile).To(Equal(expectedCorefile))

	serverBlocks, err := caddyfile.Parse("Corefile", bytes.NewReader([]byte(corefile)), nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(serverBlocks).To(HaveLen(1))
	g.Expect(serverBlocks[0].Keys).To(Equal([]string{".:53"}))
	g.Expect(serverBlocks[0].Tokens).To(HaveKey("reload"))
}

func TestReconcileCoreDNSConfigMap_CustomConfigPlaceholders(t *testing.T) {
	g, ctx, _ := G(t)
	client := fake.NewClientset()
	reconciler := newTestCoreDNSReconciler(client)

	g.Expect(reconciler.reconcileCoreDNSConfigMap(ctx)).To(Succeed())

	configMap, err := client.CoreV1().ConfigMaps(metav1.NamespaceSystem).Get(ctx, "coredns", metav1.GetOptions{})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(configMap.Data).To(Equal(map[string]string{
		"Corefile":                 expectedCorefile,
		"hcp-placeholder.server":   "",
		"hcp-placeholder.override": "",
	}))
}

func TestBuildCoreDNSVolumes_OptionalCustomConfigMap(t *testing.T) {
	g, _, _ := G(t)
	reconciler := newTestCoreDNSReconciler(fake.NewClientset())

	volumes, volumeMounts := reconciler.buildCoreDNSVolumes()

	g.Expect(volumes).To(HaveLen(2))
	g.Expect(volumeMounts).To(HaveLen(2))

	configVolume, customConfigVolume := volumes[0], volumes[1]
	g.Expect(*configVolume.ConfigMap.Name).To(Equal("coredns"))
	g.Expect(configVolume.ConfigMap.Items).To(ConsistOf(
		*corev1ac.KeyToPath().WithKey("Corefile").WithPath("Corefile"),
	))

	g.Expect(customConfigVolume.Projected).NotTo(BeNil())
	sources := customConfigVolume.Projected.Sources
	g.Expect(sources).To(HaveLen(2))
	g.Expect(*sources[0].ConfigMap.Name).To(Equal("coredns"))
	g.Expect(sources[0].ConfigMap.Optional).To(BeNil())
	g.Expect(sources[0].ConfigMap.Items).To(ConsistOf(
		*corev1ac.KeyToPath().WithKey("hcp-placeholder.server").WithPath("hcp-placeholder.server"),
		*corev1ac.KeyToPath().WithKey("hcp-placeholder.override").WithPath("hcp-placeholder.override"),
	))
	g.Expect(*sources[1].ConfigMap.Name).To(Equal("coredns-custom"))
	g.Expect(*sources[1].ConfigMap.Optional).To(BeTrue())
	g.Expect(sources[1].ConfigMap.Items).To(BeEmpty())

	g.Expect(*volumeMounts[0].Name).To(Equal(*configVolume.Name))
	g.Expect(*volumeMounts[0].MountPath).To(Equal("/etc/coredns"))
	g.Expect(*volumeMounts[1].Name).To(Equal(*customConfigVolume.Name))
	g.Expect(*volumeMounts[1].MountPath).To(Equal("/etc/coredns/custom"))
	g.Expect(*volumeMounts[1].ReadOnly).To(BeTrue())
}
