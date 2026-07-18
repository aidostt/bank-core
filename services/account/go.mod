module github.com/aidostt/bank-core/services/account

go 1.26

require (
	github.com/aidostt/bank-core/gen/go v0.0.0
	github.com/aidostt/bank-core/pkg v0.0.0
)

replace (
	github.com/aidostt/bank-core/gen/go => ../../gen/go
	github.com/aidostt/bank-core/pkg => ../../pkg
)
