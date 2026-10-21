package identity

import (
	"github.com/moomora/klyx/internal/keda"
)

// TriggerAuthSubject is who a KEDA authentication's podIdentity
// authenticates as (coverage S9 §6.3). KEDA always authenticates as its
// operator pod: operator is the handling operator's running pods'
// ServiceAccount (nil when the handler is unverified). For aws with
// identityOwner workload, KEDA reads eks.amazonaws.com/role-arn from the
// target workload's ServiceAccount (workload) and the operator assumes it.
// Cloud trust and permissions are never verified.
func TriggerAuthSubject(pi *keda.PodIdentity, operator *SARef, workload *SARef, get SALookup) Subject {
	sub := Subject{Provider: pi.Provider, Mode: "operator", Controller: true}
	owner := pi.IdentityOwner
	switch pi.Provider {
	case "aws":
		if owner == "workload" {
			sub.Mode = "workload"
			sub.Base = "the KEDA operator, assuming the role on the target workload's ServiceAccount"
			if pi.RoleARN != "" {
				sub.Refusals = append(sub.Refusals, "KEDA refuses roleArn with identityOwner workload")
			}
			sub.RoleFrom = workload
			if workload == nil {
				sub.Unresolved = append(sub.Unresolved, "the target workload's ServiceAccount could not be read")
				return withOperator(sub, operator, get)
			}
			if sa, ok := lookupSA(workload, get, &sub); ok {
				if r, has := sa.Annotations[AnnRoleARN]; has && r != "" {
					sub.Requested = append(sub.Requested, roleShown(r)+" (from ServiceAccount "+workload.Namespace+"/"+workload.Name+")")
				} else {
					sub.Refusals = append(sub.Refusals, "KEDA requires "+AnnRoleARN+" on ServiceAccount "+workload.Namespace+"/"+workload.Name+
						"; it has none, so this scaler cannot authenticate (an EKS Pod Identity association is not read by KEDA)")
				}
			}
			return withOperator(sub, operator, get)
		}
		sub.Base = "the KEDA operator's own AWS identity (its IRSA or EKS Pod Identity)"
		if pi.RoleARN != "" {
			sub.Requested = append(sub.Requested, roleShown(pi.RoleARN))
		}
	case "azure-workload":
		sub.Base = "the KEDA operator's Azure workload identity"
		if pi.IdentityID != "" {
			sub.Requested = append(sub.Requested, "client "+valueOf(pi.IdentityID, shapeGUID).Shown)
			sub.Notes = append(sub.Notes, "the client must federate with the KEDA operator's ServiceAccount")
		}
		// the tenant is part of the credential KEDA builds: an explicit one
		// is requested; none is the operator's default (implementation
		// review 1, finding 10)
		if pi.IdentityTenantID != "" {
			sub.Requested = append(sub.Requested, "tenant "+valueOf(pi.IdentityTenantID, shapeTenant).Shown)
		} else {
			sub.Notes = append(sub.Notes, "no identityTenantId: the operator's default tenant (its AZURE_TENANT_ID)")
		}
	case "gcp":
		sub.Base = "the KEDA operator's GKE Workload Identity"
	case "aws-eks":
		sub.Base = "the KEDA operator (aws-eks is deprecated; use aws)"
	case "azure", "aws-kiam":
		sub.Base = "removed in KEDA 2.15"
		sub.Refusals = append(sub.Refusals, pi.Provider+" was removed in KEDA 2.15")
		return sub
	default:
		sub.Base = "not read by Klyx's identity chain"
		return sub
	}
	if owner == "workload" {
		sub.Notes = append(sub.Notes, "identityOwner workload is ignored by "+pi.Provider)
	}
	return withOperator(sub, operator, get)
}

// withOperator records the operator's ServiceAccount as the base, or why
// it is unverified.
func withOperator(sub Subject, operator *SARef, get SALookup) Subject {
	sub.Notes = append(sub.Notes, "cloud trust and permissions are not verified")
	if operator == nil {
		sub.Unresolved = append(sub.Unresolved, "the handling KEDA operator is unverified, so its ServiceAccount is too")
		return sub
	}
	sub.SA = operator
	if get != nil {
		if _, read := get(operator.Namespace, operator.Name); read != ReadFound {
			sub.Unresolved = append(sub.Unresolved, "the KEDA operator's ServiceAccount "+operator.Namespace+"/"+operator.Name+" could not be read")
		}
	}
	return sub
}
