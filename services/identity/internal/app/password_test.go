package app

import "testing"

func TestPasswordHashRoundTrip(t *testing.T) {
	phc, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := verifyPassword("correct horse battery staple", phc)
	if err != nil || !ok {
		t.Fatalf("verify failed: %v %v", ok, err)
	}
	ok, err = verifyPassword("wrong password", phc)
	if err != nil || ok {
		t.Fatalf("wrong password accepted: %v %v", ok, err)
	}
}

func TestVerifyPasswordMalformedHash(t *testing.T) {
	if _, err := verifyPassword("x", "not-a-phc-string"); err == nil {
		t.Fatal("want error for malformed hash")
	}
}

func TestSeededAdminHash(t *testing.T) {
	// Must match the hash embedded in 000001_init.up.sql.
	const migrationHash = "$argon2id$v=19$m=19456,t=2,p=1$YmFuay1jb3JlLWRlbW8tc2FsdA$2GrBhgFqycQKm5+mhYWbynBXquRC16eBEFGvULSZ/Xo"
	ok, err := verifyPassword("Adm1n-Demo-Pass", migrationHash)
	if err != nil || !ok {
		t.Fatalf("seeded admin hash does not verify: %v %v", ok, err)
	}
}
