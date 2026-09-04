package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("util functions", func() {
	Context("computeSecretDataHash", func() {
		It("should return empty string for nil or empty map", func() {
			Expect(computeSecretDataHash(nil)).To(Equal(""))
			Expect(computeSecretDataHash(map[string][]byte{})).To(Equal(""))
		})

		It("should return deterministic hash regardless of key iteration order", func() {
			data1 := map[string][]byte{
				"database-name":     []byte("mydb"),
				"database-user":     []byte("myuser"),
				"database-password": []byte("mypassword"),
			}
			data2 := map[string][]byte{
				"database-password": []byte("mypassword"),
				"database-name":     []byte("mydb"),
				"database-user":     []byte("myuser"),
			}

			hash1 := computeSecretDataHash(data1)
			hash2 := computeSecretDataHash(data2)

			Expect(hash1).To(Not(BeEmpty()))
			Expect(hash1).To(Equal(hash2))
		})

		It("should return different hash when data changes", func() {
			data1 := map[string][]byte{
				"database-password": []byte("password123"),
			}
			data2 := map[string][]byte{
				"database-password": []byte("password456"),
			}

			hash1 := computeSecretDataHash(data1)
			hash2 := computeSecretDataHash(data2)

			Expect(hash1).To(Not(BeEmpty()))
			Expect(hash2).To(Not(BeEmpty()))
			Expect(hash1).To(Not(Equal(hash2)))
		})

		It("should alter the HMAC hash for identical payload data when database-salt is specified or changed", func() {
			payload1 := map[string][]byte{
				"database-name":     []byte("mydb"),
				"database-user":     []byte("myuser"),
				"database-password": []byte("mypassword"),
			}
			payload2 := map[string][]byte{
				"database-name":     []byte("mydb"),
				"database-user":     []byte("myuser"),
				"database-password": []byte("mypassword"),
				"database-salt":     []byte("salt-value-1"),
			}
			payload3 := map[string][]byte{
				"database-name":     []byte("mydb"),
				"database-user":     []byte("myuser"),
				"database-password": []byte("mypassword"),
				"database-salt":     []byte("salt-value-2"),
			}

			hash1 := computeSecretDataHash(payload1)
			hash2 := computeSecretDataHash(payload2)
			hash3 := computeSecretDataHash(payload3)

			Expect(hash1).To(Not(BeEmpty()))
			Expect(hash2).To(Not(BeEmpty()))
			Expect(hash3).To(Not(BeEmpty()))

			Expect(hash1).To(Not(Equal(hash2)))
			Expect(hash2).To(Not(Equal(hash3)))
			Expect(hash1).To(Not(Equal(hash3)))
		})

		It("should return empty string when data contains only database-salt", func() {
			data := map[string][]byte{
				"database-salt": []byte("salt-value"),
			}
			Expect(computeSecretDataHash(data)).To(Equal(""))
		})
	})

	Context("hfSecretsHash", func() {
		It("returns empty string when there are no sources", func() {
			Expect(hfSecretsHash(nil, nil)).To(Equal(""))
			Expect(hfSecretsHash([]HFSource{}, map[string][]byte{})).To(Equal(""))
		})

		It("is deterministic and independent of source slice order", func() {
			keys := map[string][]byte{
				"HF_API_KEY_A": []byte("aaa"),
				"HF_API_KEY_B": []byte("bbb"),
			}
			h1 := hfSecretsHash([]HFSource{
				{EnvVarName: "HF_API_KEY_A", SecretName: "s1"},
				{EnvVarName: "HF_API_KEY_B", SecretName: "s2"},
			}, keys)
			h2 := hfSecretsHash([]HFSource{
				{EnvVarName: "HF_API_KEY_B", SecretName: "s2"},
				{EnvVarName: "HF_API_KEY_A", SecretName: "s1"},
			}, keys)
			Expect(h1).To(Equal(h2))
			Expect(h1).To(Not(BeEmpty()))
		})

		It("changes when an API key value changes", func() {
			src := []HFSource{{EnvVarName: "HF_API_KEY_A", SecretName: "s1"}}
			h1 := hfSecretsHash(src, map[string][]byte{"HF_API_KEY_A": []byte("aaa")})
			h2 := hfSecretsHash(src, map[string][]byte{"HF_API_KEY_A": []byte("bbb")})
			Expect(h1).To(Not(Equal(h2)))
		})

		It("changes when a source is added or removed", func() {
			keys := map[string][]byte{"HF_API_KEY_A": []byte("aaa"), "HF_API_KEY_B": []byte("bbb")}
			h1 := hfSecretsHash([]HFSource{{EnvVarName: "HF_API_KEY_A", SecretName: "s1"}}, keys)
			h2 := hfSecretsHash([]HFSource{
				{EnvVarName: "HF_API_KEY_A", SecretName: "s1"},
				{EnvVarName: "HF_API_KEY_B", SecretName: "s2"},
			}, keys)
			Expect(h1).To(Not(Equal(h2)))
		})
	})
})
