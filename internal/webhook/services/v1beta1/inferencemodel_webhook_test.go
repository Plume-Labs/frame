package v1beta1

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	servicesv1beta1 "github.com/Plume-Labs/frame/api/services/v1beta1"
)

func TestDeletingAModelSomethingServesIsRefused(t *testing.T) {
	s := runtime.NewScheme()
	if err := servicesv1beta1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	serving := func(name, kind string) *servicesv1beta1.FrameService {
		return &servicesv1beta1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"},
			Spec: servicesv1beta1.FrameServiceSpec{
				Type:       kind,
				Parameters: map[string]framev1beta1.ParameterValue{"model": "served"},
			},
		}
	}
	v := InferenceModelCustomValidator{Reader: fake.NewClientBuilder().WithScheme(s).
		WithObjects(serving("llm", "inference"), serving("other", "postgres")).Build()}
	model := func(name string) *servicesv1beta1.InferenceModel {
		return &servicesv1beta1.InferenceModel{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}

	if _, err := v.ValidateDelete(context.Background(), model("served")); err == nil ||
		err.Error() != "inference model served is served by shop/llm" {
		t.Fatalf("got %v", err)
	}
	if _, err := v.ValidateDelete(context.Background(), model("idle")); err != nil {
		t.Fatalf("an unused model: %v", err)
	}
}
