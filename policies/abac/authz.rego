package deploy.authz

import future.keywords.if
import future.keywords.in

# Default deny
default allow := false

# Role definitions
roles := {
	"admin": {"actions": ["create_job", "list_jobs", "view_job", "delete_job"], "namespaces": ["*"]},
	"developer": {"actions": ["create_job", "list_jobs", "view_job"], "namespaces": ["sandbox", "staging"]},
	"viewer": {"actions": ["list_jobs", "view_job"], "namespaces": ["*"]},
}

# Allow if the user's role has the required action and namespace access
allow if {
    role_info := roles[input.user.role]
    input.action == role_info.actions[_]
    namespace_allowed(input.resource.namespace, role_info.namespaces)
}

# Check if the target namespace is allowed for the role
namespace_allowed(target, allowed) if {
	allowed[_] == "*"
}
namespace_allowed(target, allowed) if {
	target == allowed[_]
}

# Helper: check if user can access a specific namespace
can_access_namespace(user_role, target_ns) if {
	role_info := roles[user_role]
	namespace_allowed(target_ns, role_info.namespaces)
}
