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
	"strconv"
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
	aclRules        map[string][]AclRule
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

type AclRule struct {
	topic    string
	subtopic []AclRule
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
		data, err := ExtractACLFromFileNew(aclPath)
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
	log.Debugf("topic: " + topic)
	log.Debugf("client id: " + clientid)
	log.Debugf("acc: " + strconv.Itoa(int(acc)))
	log.Debugf("token: tha same as the login")
	if topic == "#" && acc == 4 {
		return true, nil
	}
	_, parsedTokenReturn, err := VerifyJWTSignatureAndParse(token, o.pubCertRsa) //extract claims from parsed token
	if err != nil {
		return false, err
	}
	//extract claims from parsed token
	// Extract custom claims
	claims, ok := parsedTokenReturn.Claims.(jwtGo.MapClaims)
	if !ok {
		log.Debug("invalid token claims format")
		return false, fmt.Errorf("invalid token claims format")
	}

	// Extract rules from custom claims
	rulesRaw, ok := claims["custom"].(map[string]interface{})["rules"].([]interface{})
	if !ok {
		log.Debug("rules claim not found or has invalid format")
		return false, fmt.Errorf("rules claim not found or has invalid format")
	}
	// Convert rules to string slice
	rules := make([]string, len(rulesRaw))
	for i, r := range rulesRaw {
		if s, ok := r.(string); ok {
			rules[i] = s
		} else {
			log.Debug("invalid rule format")
			return false, fmt.Errorf("invalid rule format")
		}
	}
	//divide the topic in parts
	topicParts := strings.Split(topic, "/")
	//now we have the rules from the token
	//we have to check if the rule we have is in the acl
	//if it is we have to check if the topic is in the rule
	//loop over the rules saved
	for role, aclStruct := range o.aclRules {
		//loop over the rules from the token
		for _, rule := range rules {
			//if the rule from the token is in the acl
			if role == rule {
				if checkSubtopics(topicParts, aclStruct) {
					log.Debugf("User Allowed Via ACL!!!!!")
					return true, nil
				}
			}
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

// ExtractACLFromFileNew , extracts the topics from a file and adds them to the map
func ExtractACLFromFileNew(path string) (map[string][]AclRule, error) {
	textExtracted, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	buffer := make(map[string][]AclRule)
	textExtractedString := string(textExtracted)
	//split beteween lines
	singleLines := strings.Split(textExtractedString, "\n")
	for _, currentLine := range singleLines {
		if currentLine != "" {
			//split the role and the topics and trim the spaces
			roleAndTopics := strings.Split(currentLine, ":")
			role := strings.TrimSpace(roleAndTopics[0])
			//extract the topics and trim the spaces
			TopicsFromString(roleAndTopics[1], role, buffer)
		}
	}
	return buffer, nil
}

// addSubtopic , adds a subtopic to a topic
func addSubtopic(subtopics []AclRule, subtopic []string) []AclRule {
	if len(subtopic) == 0 {
		return subtopics
	}

	var topicExists bool
	for i := range subtopics {
		if subtopics[i].topic == subtopic[0] {
			subtopics[i].subtopic = addSubtopic(subtopics[i].subtopic, subtopic[1:])
			topicExists = true
			break
		}
	}

	if !topicExists {
		AclRuleBuffer := new(AclRule)
		AclRuleBuffer.topic = subtopic[0]
		AclRuleBuffer.subtopic = addSubtopic(nil, subtopic[1:])
		subtopics = append(subtopics, *AclRuleBuffer)
	}

	return subtopics
}

// TopicsFromString , returns a map of topics and subtopics from a string example topic1/topic2/topic3,topic4/topic5
func TopicsFromString(topic string, role string, buffer map[string][]AclRule) map[string][]AclRule {
	//from here reusable if you have a string
	topics := strings.Split(strings.TrimSpace(topic), ",")
	for _, currentExaminedTopic := range topics {
		//split the topic and the subtopics
		currentExaminedTopicSplitted := strings.Split(currentExaminedTopic, "/")
		//Add topic to buffer
		if len(currentExaminedTopicSplitted) == 1 {
			AclRuleBuffer := new(AclRule)
			AclRuleBuffer.topic = currentExaminedTopicSplitted[0]
			AclRuleBuffer.subtopic = nil
			buffer[role] = append(buffer[role], *AclRuleBuffer)
		} else {
			//Add subtopic to buffer
			subtopicBuffer := addSubtopic(buffer[role], currentExaminedTopicSplitted)
			buffer[role] = subtopicBuffer
		}
	}
	return buffer
}

// checkSubtopics checks if the given topics are covered by the given ACL rules
func checkSubtopics(topics []string, aclStructs []AclRule) bool {
	// Se la slice di topics è vuota, allora abbiamo trovato la regola corretta
	if len(topics) == 0 {
		return true
	}
	// Prendiamo il primo sotto-topic dalla slice
	subtopic := topics[0]

	// Controlliamo se c'è una regola che copre tutti i topic utilizzando il carattere jolly #
	for _, aclStruct := range aclStructs {
		if aclStruct.topic == "#" {
			return true
		}
	}
	// Cerchiamo il sotto-topic all'interno delle strutture AclRule
	for _, aclStruct := range aclStructs {
		// Se il sotto-topic corrisponde alla wildcard "#", allora tutti i sotto-topics successivi sono permessi
		if aclStruct.topic == "#" {
			return true
		}

		// Se il sotto-topic corrisponde, continuamo a cercare nella gerarchia dei sotto-topics
		if aclStruct.topic == subtopic {
			// Continuiamo la ricerca nella gerarchia dei sotto-topics
			return checkSubtopics(topics[1:], aclStruct.subtopic)
		}
	}

	// Se non abbiamo trovato il sotto-topic corrispondente, allora la regola non è valida
	return false
}
