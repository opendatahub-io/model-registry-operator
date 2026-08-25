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
})
