DOMAIN=$1
if [ -z ${DOMAIN} ]; then
  echo "Missing command line parameter for certificate domain"
  exit 1
fi
mkdir -p certs
# create CA cert
openssl req -x509 -sha256 -nodes -days 365 -newkey rsa:2048 -subj "/O=modelregistry Inc./CN=$DOMAIN" -keyout certs/domain.key -out certs/domain.crt


# create DB service cert and private key
echo "subjectAltName = DNS:model-registry-db" > certs/model-registry-db.ext
openssl req -out certs/model-registry-db.csr -newkey rsa:2048 -nodes -keyout certs/model-registry-db.key -subj "/CN=model-registry-db/O=modelregistry organization" -addext "subjectAltName = DNS:model-registry-db"
openssl x509 -req -sha256 -days 365 -CA certs/domain.crt -CAkey certs/domain.key -set_serial 0 -in certs/model-registry-db.csr -out certs/model-registry-db.crt -extfile certs/model-registry-db.ext
