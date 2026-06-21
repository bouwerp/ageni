package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

func generateZhipuJWT(apikey string) string {
	parts := strings.Split(apikey, ".")
	if len(parts) != 2 {
		return apikey
	}
	id, secret := parts[0], parts[1]

	now := time.Now().UnixMilli()
	exp := now + 3600*1000

	header := `{"alg":"HS256","sign_type":"SIGN"}`
	payload := fmt.Sprintf(`{"api_key":"%s","exp":%d,"timestamp":%d}`, id, exp, now)

	encodedHeader := base64.RawURLEncoding.EncodeToString([]byte(header))
	encodedPayload := base64.RawURLEncoding.EncodeToString([]byte(payload))

	unsignedToken := encodedHeader + "." + encodedPayload

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(unsignedToken))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return unsignedToken + "." + signature
}

func main() {
	tok := generateZhipuJWT("testId.testSecret")
	fmt.Println(tok)
}
