package backends

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	jwtGo "github.com/golang-jwt/jwt"
	log "github.com/sirupsen/logrus"
	"io"
	"net/http"
	"os"
	"strings"
)

// goJWTChecker main struct
type goJWTChecker struct {
	pubCertRsaPath string
	issuerURL      string
	options        tokenOptions
	//allowedRoles   []string
	allowedIssuer []string
	parsedToken   *jwtGo.Token
	//pubCertRsa allowed PublicCert for rs256 verification
	pubCertRsa      []*rsa.PublicKey
	kid             []string
	allowedAudience []string
	aclRules        map[string][]string
}

// MainJSON main structure of cloudflare JSON
type MainJSON struct {
	Keys        []keys       `json:"keys"`
	PublicCert  publicCert   `json:"public_cert"`
	PublicCerts []publicCert `json:"public_certs"`
}

// structure of keys field
type keys struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	E   string `json:"e"`
	N   string `json:"n"`
}

// structure of both publicCert fields
type publicCert struct {
	Kid  string `json:"kid"`
	Cert string `json:"cert"`
}

func NewGoBckChecker(authOpts map[string]string, options tokenOptions) (jwtChecker, error) {
	checker := &goJWTChecker{
		options: options,
	}
	//kid to load the public certificate
	if kidPath, ok := authOpts["jwt_go_kid_path"]; ok {
		//extract data from file
		data, err := ExtractDataFromFile(kidPath)
		if err != nil {
			return nil, err
		}
		checker.kid = append(checker.kid, data...) //append a slice to a slice
	} else {
		log.Debug("please specify kid")
		return nil, fmt.Errorf("not specified kid")
	}
	//audience to verify if the certificate is for me
	if audPath, ok := authOpts["jwt_go_audience_path"]; ok {
		data, err := ExtractDataFromFile(audPath)
		if err != nil {
			return nil, err
		}
		checker.allowedAudience = append(checker.allowedAudience, data...)
	} else {
		log.Debug("please specify audience")
		return nil, fmt.Errorf("not specified audience")
	}
	//acl rule verify the user permissions based on role
	if aclPath, ok := authOpts["jwt_go_acl_path"]; ok {
		data, err := ExtractACLFromFile(aclPath)
		if err != nil {
			return nil, err
		}
		checker.aclRules = data
	} else {
		log.Debug("please specify acl")
		return nil, fmt.Errorf("not specified acl")
	}
	//public certificate path has to be in the pem format
	if pubCertPath, ok := authOpts["jwt_go_pubcert_path_RSA"]; ok {
		log.Debugf("Path given to go-auth: -> " + pubCertPath)
		data, err := os.ReadFile(pubCertPath)
		if err != nil {
			log.Debugf("Error during file reading %s", err)
			return nil, err
		}
		//converting string gain from file to *rsa.PublicKey format
		pubCertConverted, err := StringToRSAPublicKey(data)
		if err != nil {
			log.Debugf("Error in certificate conversion %s", err)
			return nil, err
		} else {
			checker.pubCertRsa = append(checker.pubCertRsa, pubCertConverted)
		}
	}
	//link to public certificate
	if link, ok := authOpts["jwt_go_pubcert_link"]; ok {
		checker.issuerURL = link
		pubCertExtracted, err := GetPubCertFromURL(link, checker.kid)
		if err != nil {
			return nil, fmt.Errorf("error during public cert extracting")
		}
		checker.pubCertRsa = append(checker.pubCertRsa, pubCertExtracted)

	}
	//kid value to extract the key
	if checker.pubCertRsa == nil {
		log.Debug("please provide at least one source of certificate")
		return nil, fmt.Errorf("empty public certificate")
	}
	/*
		//allowed role from token claims
		if roles, ok := authOpts["jwt_go_allowed_role"]; ok {
			checker.allowedRoles = append(checker.allowedRoles, roles)
		} else {
			log.Debug("please specify allowed rules")
			return nil, fmt.Errorf("not specified rule")
		}
	*/
	//allowed issuer
	if issPath, ok := authOpts["jwt_go_allowed_iss_path"]; ok {
		data, err := ExtractDataFromFile(issPath)
		if err != nil {
			return nil, err
		}
		checker.allowedIssuer = append(checker.allowedIssuer, data...)
	} else {
		log.Debug("please specify Iss")
		return nil, fmt.Errorf("not specified iss")
	}
	return checker, nil
}

func (o *goJWTChecker) GetSuperuser(token string) (bool, error) {
	return false, nil
}

func (o *goJWTChecker) CheckAcl(token, topic, clientid string, acc int32) (bool, error) {
	log.Debugf(topic)
	_, parsedTokenReturn, err := VerifyJWTSignatureAndParse(token, o.pubCertRsa) //extract claims from parsed token
	if err != nil {
		return false, err
	}
	//extract claims from parsed token
	if claims, ok := parsedTokenReturn.Claims.(jwtGo.MapClaims); ok {
		//extract custom claim
		if custom, ok := claims["custom"].(map[string]interface{}); ok {
			//extract rules
			if rules, ok := custom["rules"].([]interface{}); ok {
				//loop through all the rules from the token
				for _, currentRule := range rules {
					//loop drought all the rules from the acl
					for currentAllowedRole, rolePerTopic := range o.aclRules {
						//if the rule from the token is equal to the rule from the acl
						if currentRule == currentAllowedRole {
							//loop through all the allowed roles from the acl
							for _, allowedTopic := range rolePerTopic {
								//if the allowed topic from the acl is 100% equal to the topic OR # for all topics
								if allowedTopic == topic || allowedTopic == "#" {
									return true, nil
								}
								//if the allowed topic contains a / it means that we are allowed only for some subtopics
								if strings.Contains(allowedTopic, "/") {
									mainTopicAllowed := strings.Split(allowedTopic, "/")
									mainTopicRecived := strings.Split(topic, "/")
									//if the main topic is equal
									if mainTopicAllowed[0] == mainTopicRecived[0] {

									}
								}

							}
							log.Debugf("acl not passed, no allowed topic")
						}
					}
				}
			} else {
				log.Debug("rules claim not found")
				return false, fmt.Errorf("rules claim not found")
			}
		} else {
			log.Debug("custom claim not found")
			return false, fmt.Errorf("custom claim not found")
		}
	}

	return false, nil
}

func (o *goJWTChecker) GetUser(token string) (bool, error) {
	//params := map[string]interface{}{
	//	"token": token,
	//}
	//Verify the token and if valid parse it and return the parsed token
	valid, parsedTokenReturn, err := VerifyJWTSignatureAndParse(token, o.pubCertRsa)
	if err != nil || valid == false {
		log.Debugf("go error : #{err}")
		return false, err
	}
	o.parsedToken = parsedTokenReturn
	//Check the claims for allowed issuer and audience
	parsed, err := CheckAudiIssClaims(parsedTokenReturn, o.allowedIssuer, o.allowedAudience)
	return parsed, err
}

func (o *goJWTChecker) Halt() {
	// NO-OP
}

// VerifyJWTSignatureAndParse Function to check if the signature is valid given a slice of publicKey (if too much could be slow) gives back if is valid and the parsed token
func VerifyJWTSignatureAndParse(tokenStr string, publicKey []*rsa.PublicKey) (bool, *jwtGo.Token, error) {
	// Parse the token
	var err error
	var token *jwtGo.Token
	for _, publicKeyFor := range publicKey {
		token, err = jwtGo.Parse(tokenStr, func(token *jwtGo.Token) (interface{}, error) {
			// Check the sign method
			if _, ok := token.Method.(*jwtGo.SigningMethodRSA); ok {
				log.Debugf("Signing method RSA")
				return publicKeyFor, nil
			}
			log.Debugf("sign method not valid")
			return nil, fmt.Errorf("sign method not valid")
		})
		if token != nil {
			if token.Valid {
				return true, token, nil
			}
		} else {
			log.Debugf("token not valid skipped check if token.valid")
		}

		if err != nil {
			log.Debug("error from looping the pub certs: ", err)
		}
	}
	return false, nil, err
}

// StringToRSAPublicKey returns *rsa.PublicKey type variable given a slice of byte
func StringToRSAPublicKey(publicKeyStr []byte) (*rsa.PublicKey, error) {
	// Parse the PEM pub key
	block, _ := pem.Decode(publicKeyStr)
	if block == nil {
		return nil, fmt.Errorf("error decoding public key")
	}
	// Public key parsing
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		log.Debug("error during public key parsing:", err)
		return nil, err
	}
	rsaPublicKey := cert.PublicKey.(*rsa.PublicKey)
	//fmt.Println("Rsa Pub Key N", rsaPublicKey.N)
	//fmt.Println("Rsa Pub Key E", rsaPublicKey.E)
	return rsaPublicKey, nil
}

// CheckAudiIssClaims check if claims are ok like iss and user role
func CheckAudiIssClaims(parsedToken *jwtGo.Token, allowedIssuer []string, allowedAudience []string) (bool, error) {
	var claims jwtGo.MapClaims
	var ok bool
	var audok = false
	var issok = false
	if claims, ok = parsedToken.Claims.(jwtGo.MapClaims); ok {
		if aud, ok := claims["aud"].([]interface{}); ok {
			for _, allowedAud := range allowedAudience {
				if aud[0] == allowedAud { //implement audition key
					audok = true
					log.Debug("audience ok")
				} else {
					log.Debug("audience ! ok")
					return false, fmt.Errorf("not allowed audience")
				}
			}
		}
	} else {
		log.Debug("unable to access claim field")
	}
	if iss, ok := claims["iss"].(string); ok {
		//checking the allowed issuer if there is more than one
		for _, allowedIss := range allowedIssuer {
			if iss == allowedIss {
				issok = true
				log.Debug("iss claim ok")
			} else {
				log.Debug("iss claim ! ok")
				return false, nil
			}
		}
	} else {
		log.Debug("iss claim not a string")
	}

	if issok && audok {
		return true, nil
	}
	return false, fmt.Errorf("unpredict exit")
}

// GetPubCertFromURL get a public certificate from a JSON via URL
func GetPubCertFromURL(url string, kid []string) (*rsa.PublicKey, error) {
	response, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("error during get request")
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Errorf("error during body close %e", err)
			return
		}
	}(response.Body)

	//read body response
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("error during response resding %e", err)
	}

	//decode message body
	var DecodedJson MainJSON
	err = json.Unmarshal(body, &DecodedJson)
	if err != nil {
		return nil, fmt.Errorf("error during message decoding %e", err)
	}
	//fmt.Println(DecodedJson)

	//extract the public key from the selected kid
	for _, certs := range DecodedJson.PublicCerts {
		for _, loopedKid := range kid {
			if certs.Kid == loopedKid {
				//returns the public key requested and the error from the called function
				return StringToRSAPublicKey([]byte(certs.Cert))
			}
		}
	}
	return nil, fmt.Errorf("error kid not found")
}

// ExtractDataFromFile , returns []string divider is \n
func ExtractDataFromFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	str := string(data)
	values := strings.Split(str, "\n")
	var stringSlice []string
	for _, value := range values {
		stringSlice = append(stringSlice, value)
	}
	return stringSlice, nil
}

/*
ExtractACLFromFile , returns map[string][]string divider between role and topic is :, the divider between topics is ,
data example: role1:$SYS/#, topic1, topic2/#

	role2:topic1/uno, topic1/due

data structure saved: map[

			role1:[$SYS/# topic1 topic2/#]
			role2:[topic1 topic2]
	   ]

data structure to implement now

	topics := map[string]map[string][]string{
	        "$SYS": {
	            "status":     {"#"},
	            "connections": {"#"},
	        },
	        "luci": {
	            "soggiorno":  {"on", "off"},
	            "cucina":     {"on", "off", "dimmer"},
	            "camera letto": {"on", "off", "intensita"},
	        },
	        "prese": {
	            "soggiorno":  {"on", "off"},
	            "cucina":     {"on", "off"},
	            "camera letto": {"on", "off", "timer"},
	        },
	    }

			source string:
				user1:$SYS/status/
*/
func ExtractACLFromFile(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	str := string(data)
	roleToTopic := make(map[string][]string)
	lines := strings.Split(str, "\n")
	for _, line := range lines {
		if line != "" {
			//Split the linr by the colon separator
			parts := strings.Split(line, ":")
			//Exctract the role and trim the spaces
			role := strings.TrimSpace(parts[0])
			//Extract the topics and trim the spaces
			topics := strings.Split(strings.TrimSpace(parts[1]), ",")
			//Trim the spaces from the topics
			for i, topic := range topics {
				topics[i] = strings.TrimSpace(topic)
			}
			//add the role and the topics to the map
			roleToTopic[role] = topics
		}
	}

	return roleToTopic, nil
}
