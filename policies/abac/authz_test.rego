package deploy.authz

test_admin_can_create_job_in_any_namespace if {
	allow with input as {"user": {"role": "admin"}, "action": "create_job", "resource": {"namespace": "production"}}
	allow with input as {"user": {"role": "admin"}, "action": "create_job", "resource": {"namespace": "sandbox"}}
	allow with input as {"user": {"role": "admin"}, "action": "create_job", "resource": {"namespace": "staging"}}
}

test_developer_can_create_job_in_sandbox if {
	allow with input as {"user": {"role": "developer"}, "action": "create_job", "resource": {"namespace": "sandbox"}}
}

test_developer_cannot_create_job_in_production if {
	not allow with input as {"user": {"role": "developer"}, "action": "create_job", "resource": {"namespace": "production"}}
}

test_viewer_cannot_create_job if {
	not allow with input as {"user": {"role": "viewer"}, "action": "create_job", "resource": {"namespace": "sandbox"}}
}

test_viewer_can_list_jobs if {
	allow with input as {"user": {"role": "viewer"}, "action": "list_jobs", "resource": {"namespace": "sandbox"}}
}
