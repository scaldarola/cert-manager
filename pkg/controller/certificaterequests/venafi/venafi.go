/*
Copyright 2019 The Jetstack cert-manager contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package venafi

import (
	"context"

	"github.com/Venafi/vcert/pkg/endpoint"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/record"

	apiutil "github.com/jetstack/cert-manager/pkg/api/util"
	cmapi "github.com/jetstack/cert-manager/pkg/apis/certmanager/v1alpha1"
	controllerpkg "github.com/jetstack/cert-manager/pkg/controller"
	"github.com/jetstack/cert-manager/pkg/controller/certificaterequests"
	crutil "github.com/jetstack/cert-manager/pkg/controller/certificaterequests/util"
	venafiinternal "github.com/jetstack/cert-manager/pkg/internal/venafi"
	issuerpkg "github.com/jetstack/cert-manager/pkg/issuer"
	logf "github.com/jetstack/cert-manager/pkg/logs"
)

const (
	CRControllerName = "certificaterequests-issuer-venafi"
)

type Venafi struct {
	// used to record Events about resources to the API
	recorder record.EventRecorder

	issuerOptions controllerpkg.IssuerOptions
	secretsLister corelisters.SecretLister
	helper        issuerpkg.Helper

	clientBuilder venafiinternal.VenafiClientBuilder
}

func init() {
	// create certificate request controller for venafi issuer
	controllerpkg.Register(CRControllerName, func(ctx *controllerpkg.Context) (controllerpkg.Interface, error) {
		venafi := NewVenafi(ctx)

		controller := certificaterequests.New(apiutil.IssuerVenafi, venafi)

		c, err := controllerpkg.New(ctx, CRControllerName, controller)
		if err != nil {
			return nil, err
		}

		return c.Run, nil
	})
}

func NewVenafi(ctx *controllerpkg.Context) *Venafi {
	return &Venafi{
		recorder:      ctx.Recorder,
		issuerOptions: ctx.IssuerOptions,
		secretsLister: ctx.KubeSharedInformerFactory.Core().V1().Secrets().Lister(),
		helper: issuerpkg.NewHelper(
			ctx.SharedInformerFactory.Certmanager().V1alpha1().Issuers().Lister(),
			ctx.SharedInformerFactory.Certmanager().V1alpha1().ClusterIssuers().Lister(),
		),
		clientBuilder: venafiinternal.New,
	}
}

func (v *Venafi) Sign(ctx context.Context, cr *cmapi.CertificateRequest, issuerObj cmapi.GenericIssuer) (*issuerpkg.IssueResponse, error) {
	log := logf.FromContext(ctx, "sign")
	reporter := crutil.NewReporter(cr, v.recorder)

	client, err := v.clientBuilder(cr.Namespace, v.secretsLister, issuerObj)
	if err != nil {
		log = logf.WithRelatedResource(log, issuerObj)

		if k8sErrors.IsNotFound(err) {
			message := "Required secret resource not found"

			reporter.Pending(err, "MissingSecret", message)
			log.Error(err, message)

			return nil, nil
		}

		message := "Failed to initialise venafi client for signing"
		reporter.Pending(err, "ErrorVenafiInit", message)
		log.Error(err, message)

		return nil, err
	}

	duration := apiutil.DefaultCertDuration(cr.Spec.Duration)

	certPem, err := client.Sign(cr.Spec.CSRPEM, duration)

	// Check some known error types
	if err != nil {
		switch err.(type) {

		case endpoint.ErrCertificatePending:
			message := "venafi certificate still in a pending state, the request will be retried"

			reporter.Pending(err, "IssuancePending", message)
			log.Error(err, message)
			return nil, err

		case endpoint.ErrRetrieveCertificateTimeout:
			message := "timed out waiting for venafi certificate, the request will be retried"

			reporter.Failed(err, "Timeout", message)
			log.Error(err, message)
			return nil, nil

		default:
			message := "failed to obtain venafi certificate"

			reporter.Pending(err, "Retrieve", message)
			log.Error(err, message)

			return nil, err
		}
	}

	log.Info("certificate issued")

	return &issuerpkg.IssueResponse{
		Certificate: certPem,
	}, nil
}
