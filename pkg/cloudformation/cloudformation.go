package cloudformation

import (
	"fmt"
	"sort"
	"strings"

	"github.com/awslabs/amazon-apigateway-ingress-controller/pkg/network"
	cfn "github.com/awslabs/goformation/cloudformation"
	"github.com/awslabs/goformation/cloudformation/resources"

	extensionsv1beta1 "k8s.io/api/extensions/v1beta1"
)

const (
	OutputKeyRestApiID             = "RestAPIID"
	OutputKeyAPIGatewayEndpoint    = "APIGatewayEndpoint"
	OutputKeyClientARNS            = "ClientARNS"
	OutputKeyAPIGatewayWSSEndpoint = "OutputKeyAPIGatewayWSSEndpoint"
)

func toLogicalName(idx int, parts []string) string {
	s := strings.Join(parts[:idx+1], "")
	remove := []string{"{", "}", "+"}
	for _, char := range remove {
		s = strings.Replace(s, char, "", -1)
	}
	return s
}

func toPath(idx int, parts []string) string {
	if parts[idx] == "{proxy+}" {
		return strings.Join(parts[:idx], "/") + "/{proxy}"
	}
	return strings.Join(parts[:idx+1], "/")
}

func mapApiGatewayMethodsAndResourcesFromPaths(paths []extensionsv1beta1.HTTPIngressPath) map[string]cfn.Resource {
	m := map[string]cfn.Resource{}

	for _, path := range paths {
		parts := strings.Split(path.Path, "/")
		parts = append(parts, "{proxy+}")
		for idx, part := range parts {
			if idx == 0 {
				continue
			}
			ref := cfn.GetAtt("RestAPI", "RootResourceId")
			if idx > 1 {
				ref = cfn.Ref(fmt.Sprintf("Resource%s", toLogicalName(idx-1, parts)))
			}

			resourceLogicalName := fmt.Sprintf("Resource%s", toLogicalName(idx, parts))
			m[resourceLogicalName] = buildAWSApiGatewayResource(ref, part)
			m[fmt.Sprintf("Method%s", toLogicalName(idx, parts))] = buildAWSApiGatewayMethod(resourceLogicalName, toPath(idx, parts))
		}
	}

	return m
}

func buildAWSApiGatewayResource(ref, part string) *resources.AWSApiGatewayResource {
	return &resources.AWSApiGatewayResource{
		ParentId:  ref,
		PathPart:  part,
		RestApiId: cfn.Ref("RestAPI"),
	}
}

func buildAWSApiGatewayRestAPI(arns []string) *resources.AWSApiGatewayRestApi {
	return &resources.AWSApiGatewayRestApi{
		ApiKeySourceType: "HEADER",
		EndpointConfiguration: &resources.AWSApiGatewayRestApi_EndpointConfiguration{
			Types: []string{"EDGE"},
		},
		Name: cfn.Ref("AWS::StackName"),
	}
}

func buildAWSApiGatewayWebSocketAPI() *resources.AWSApiGatewayV2Api {
	return &resources.AWSApiGatewayV2Api{
		Name:                     fmt.Sprintf("%s-websocket", cfn.Ref("AWS::StackName")),
		ProtocolType:             "WEBSOCKET",
		RouteSelectionExpression: "$request.body.action",
	}
}

func buildAWSApiGatewayAuthorizer(CognitoUserPoolArns []string) *resources.AWSApiGatewayAuthorizer {
	return &resources.AWSApiGatewayAuthorizer{
		RestApiId:      cfn.Ref("RestAPI"),
		Name:           "Cognito-Authorizer",
		Type:           "COGNITO_USER_POOLS",
		IdentitySource: "method.request.header.Authorization",
		ProviderARNs:   CognitoUserPoolArns,
	}
}

func buildAWSApiGatewayDeployment(stageName string, dependsOn []string) *resources.AWSApiGatewayDeployment {
	d := &resources.AWSApiGatewayDeployment{
		RestApiId: cfn.Ref("RestAPI"),
		StageName: stageName,
	}

	// Since we construct a map of in `mapApiGatewayMethodsAndResourcesFromPaths` we can't determine the order
	// that this list will be in - making it difficult to test - the order isn't important - but passing tests are.
	// This isn't the worst thing in the world - and - I considered refactoring - but I like how simple this is for now.
	// Also the order doesn't matter to CFN in the end.
	sort.Strings(dependsOn)
	d.SetDependsOn(dependsOn)

	return d
}

func buildAWSElasticLoadBalancingV2Listener() *resources.AWSElasticLoadBalancingV2Listener {
	return &resources.AWSElasticLoadBalancingV2Listener{
		LoadBalancerArn: cfn.Ref("LoadBalancer"),
		Protocol:        "TCP",
		Port:            80,
		DefaultActions: []resources.AWSElasticLoadBalancingV2Listener_Action{
			resources.AWSElasticLoadBalancingV2Listener_Action{
				TargetGroupArn: cfn.Ref("TargetGroup"),
				Type:           "forward",
			},
		},
	}
}

func buildAWSElasticLoadBalancingV2LoadBalancer(subnetIDs []string) *resources.AWSElasticLoadBalancingV2LoadBalancer {
	return &resources.AWSElasticLoadBalancingV2LoadBalancer{
		IpAddressType: "ipv4",
		Scheme:        "internal",
		Subnets:       subnetIDs,
		Tags: []resources.Tag{
			{
				Key:   "com.github.amazon-apigateway-ingress-controller/stack",
				Value: cfn.Ref("AWS::StackName"),
			},
		},
		Type: "network",
	}
}

func buildAWSElasticLoadBalancingV2TargetGroup(vpcID string, instanceIDs []string, nodePort int, dependsOn []string) *resources.AWSElasticLoadBalancingV2TargetGroup {
	targets := make([]resources.AWSElasticLoadBalancingV2TargetGroup_TargetDescription, len(instanceIDs))
	for i, instanceID := range instanceIDs {
		targets[i] = resources.AWSElasticLoadBalancingV2TargetGroup_TargetDescription{Id: instanceID}
	}

	return &resources.AWSElasticLoadBalancingV2TargetGroup{
		HealthCheckIntervalSeconds: 30,
		HealthCheckPort:            "traffic-port",
		HealthCheckProtocol:        "TCP",
		HealthCheckTimeoutSeconds:  10,
		HealthyThresholdCount:      3,
		Port:                       nodePort,
		Protocol:                   "TCP",
		Tags: []resources.Tag{
			{
				Key:   "com.github.amazon-apigateway-ingress-controller/stack",
				Value: cfn.Ref("AWS::StackName"),
			},
		},
		TargetType:              "instance",
		Targets:                 targets,
		UnhealthyThresholdCount: 3,
		VpcId:                   vpcID,
	}

}

func buildAWSApiGatewayVpcLink(dependsOn []string) *resources.AWSApiGatewayVpcLink {
	r := &resources.AWSApiGatewayVpcLink{
		Name:       cfn.Ref("AWS::StackName"),
		TargetArns: []string{cfn.Ref("LoadBalancer")},
	}

	r.SetDependsOn(dependsOn)

	return r
}

func buildAWSApiGatewayMethod(resourceLogicalName, path string) *resources.AWSApiGatewayMethod {
	m := &resources.AWSApiGatewayMethod{
		RequestParameters: map[string]bool{
			"method.request.path.proxy": true,
		},
		AuthorizationType: "COGNITO_USER_POOLS",
		HttpMethod:        "ANY",
		AuthorizerId:      cfn.Ref("CognitoAuthorizer"),
		ResourceId:        cfn.Ref(resourceLogicalName),
		RestApiId:         cfn.Ref("RestAPI"),
		Integration: &resources.AWSApiGatewayMethod_Integration{
			ConnectionId:          cfn.Ref("VPCLink"),
			ConnectionType:        "VPC_LINK",
			IntegrationHttpMethod: "ANY",
			PassthroughBehavior:   "WHEN_NO_MATCH",
			RequestParameters: map[string]string{
				"integration.request.path.proxy":             "method.request.path.proxy",
				"integration.request.header.Accept-Encoding": "'identity'",
			},
			Type:            "HTTP_PROXY",
			TimeoutInMillis: 29000,
			Uri:             cfn.Join("", []string{"http://", cfn.GetAtt("LoadBalancer", "DNSName"), path}),
		},
	}

	m.SetDependsOn([]string{"LoadBalancer", "CognitoAuthorizer"})
	return m
}

func buildAWSEC2SecurityGroupIngresses(securityGroupIds []string, cidr string, nodePort int) []*resources.AWSEC2SecurityGroupIngress {
	sgIngresses := make([]*resources.AWSEC2SecurityGroupIngress, len(securityGroupIds))
	for i, sgID := range securityGroupIds {
		sgIngresses[i] = &resources.AWSEC2SecurityGroupIngress{
			IpProtocol: "TCP",
			CidrIp:     cidr,
			FromPort:   nodePort,
			ToPort:     nodePort,
			GroupId:    sgID,
		}
	}

	return sgIngresses
}

func buildCustomDomain(domainName, certificateArn string) *resources.AWSApiGatewayDomainName {
	return &resources.AWSApiGatewayDomainName{
		CertificateArn: certificateArn,
		DomainName:     domainName,
		EndpointConfiguration: &resources.AWSApiGatewayDomainName_EndpointConfiguration{
			Types: []string{"EDGE"},
		},
	}
}

func buildAWSApiGatewayWSSRoute() *resources.AWSApiGatewayV2Route {
	return &resources.AWSApiGatewayV2Route{
		ApiId:             cfn.Ref("webSocketAPI"),
		RouteKey:          "$default",
		AuthorizationType: "NONE",
		Target:            cfn.Join("/", []string{"integrations", cfn.Ref("webSocketIntegration")}),
	}
}

func buildAWSAPIGatewayWSSIntegration() *resources.AWSApiGatewayV2Integration {
	return &resources.AWSApiGatewayV2Integration{
		ApiId:               cfn.Ref("webSocketAPI"),
		ConnectionId:        cfn.Ref("VPCLink"),
		ConnectionType:      "VPC_LINK",
		IntegrationMethod:   "ANY",
		PassthroughBehavior: "WHEN_NO_MATCH",
		// RequestParameters: map[string]string{
		// 	"integration.request.path.proxy":             "method.request.path.proxy",
		// 	"integration.request.header.Accept-Encoding": "'identity'",
		// },
		IntegrationType: "HTTP_PROXY",
		TimeoutInMillis: 29000,
		IntegrationUri:  cfn.Join("", []string{"http://", cfn.GetAtt("LoadBalancer", "DNSName"), "/"}),
	}
}

func buildAWSAPIGatewayWSSIntegrationResponse() *resources.AWSApiGatewayV2IntegrationResponse {
	return &resources.AWSApiGatewayV2IntegrationResponse{
		ApiId:                  cfn.Ref("webSocketAPI"),
		IntegrationId:          cfn.Ref("webSocketIntegration"),
		IntegrationResponseKey: "$default",
	}
}

func buildAWSAPIGatewayWSSDeployment(stageName string) *resources.AWSApiGatewayV2Deployment {
	d := &resources.AWSApiGatewayV2Deployment{
		ApiId: cfn.Ref("webSocketAPI"),
		// StageName: stageName,
	}
	d.SetDependsOn([]string{"webSocketIntegration", "webSocketAPI", "webSocketDefaultRoute", "webSocketIntegrationResponse"})
	return d
}

func buildAWSAPIGatewayWSSStage(stageName string) *resources.AWSApiGatewayV2Stage {
	s := &resources.AWSApiGatewayV2Stage{
		ApiId:        cfn.Ref("webSocketAPI"),
		StageName:    stageName,
		DeploymentId: cfn.Ref("webSocketDeployment"),
	}
	return s
}

type TemplateConfig struct {
	Network             *network.Network
	Rule                extensionsv1beta1.IngressRule
	NodePort            int
	StageName           string
	Arns                []string
	CognitoUserPoolArns []string
	CustomDomainName    string
	CertificateArn      string
}

func BuildApiGatewayTemplateFromIngressRule(cfg *TemplateConfig) *cfn.Template {
	template := cfn.NewTemplate()
	paths := cfg.Rule.IngressRuleValue.HTTP.Paths

	methodLogicalNames := []string{}
	resourceMap := mapApiGatewayMethodsAndResourcesFromPaths(paths)
	for k, resource := range resourceMap {
		if _, ok := resource.(*resources.AWSApiGatewayMethod); ok {
			methodLogicalNames = append(methodLogicalNames, k)
		}

		template.Resources[k] = resource
	}

	targetGroup := buildAWSElasticLoadBalancingV2TargetGroup(*cfg.Network.Vpc.VpcId, cfg.Network.InstanceIDs, cfg.NodePort, []string{"LoadBalancer"})
	template.Resources["TargetGroup"] = targetGroup

	listener := buildAWSElasticLoadBalancingV2Listener()
	template.Resources["Listener"] = listener

	securityGroupIngresses := buildAWSEC2SecurityGroupIngresses(cfg.Network.SecurityGroupIDs, *cfg.Network.Vpc.CidrBlock, cfg.NodePort)
	for i, sgI := range securityGroupIngresses {
		template.Resources[fmt.Sprintf("SecurityGroupIngress%d", i)] = sgI
	}

	restAPI := buildAWSApiGatewayRestAPI(cfg.Arns)
	template.Resources["RestAPI"] = restAPI

	webSocketAPI := buildAWSApiGatewayWebSocketAPI()
	template.Resources["webSocketAPI"] = webSocketAPI

	webSocketIntegration := buildAWSAPIGatewayWSSIntegration()
	template.Resources["webSocketIntegration"] = webSocketIntegration

	webSocketDefaultRoute := buildAWSApiGatewayWSSRoute()
	template.Resources["webSocketDefaultRoute"] = webSocketDefaultRoute

	webSocketIntegrationResponse := buildAWSAPIGatewayWSSIntegrationResponse()
	template.Resources["webSocketIntegrationResponse"] = webSocketIntegrationResponse

	webSocketDeployment := buildAWSAPIGatewayWSSDeployment(cfg.StageName)
	template.Resources["webSocketDeployment"] = webSocketDeployment

	webSocketStage := buildAWSAPIGatewayWSSStage(cfg.StageName)
	template.Resources["webSocketStage"] = webSocketStage

	cognitoAuthorizer := buildAWSApiGatewayAuthorizer(cfg.CognitoUserPoolArns)
	template.Resources["CognitoAuthorizer"] = cognitoAuthorizer

	deployment := buildAWSApiGatewayDeployment(cfg.StageName, methodLogicalNames)
	template.Resources["Deployment"] = deployment

	loadBalancer := buildAWSElasticLoadBalancingV2LoadBalancer(cfg.Network.SubnetIDs)
	template.Resources["LoadBalancer"] = loadBalancer

	vPCLink := buildAWSApiGatewayVpcLink([]string{"LoadBalancer"})
	template.Resources["VPCLink"] = vPCLink

	if cfg.CustomDomainName != "" && cfg.CertificateArn != "" {
		customDomain := buildCustomDomain(cfg.CustomDomainName, cfg.CertificateArn)
		template.Resources["CustomDomain"] = customDomain
	}

	template.Outputs = map[string]interface{}{
		OutputKeyRestApiID:             Output{Value: cfn.Ref("RestAPI")},
		OutputKeyAPIGatewayEndpoint:    Output{Value: cfn.Join("", []string{"https://", cfn.Ref("RestAPI"), ".execute-api.", cfn.Ref("AWS::Region"), ".amazonaws.com/", cfg.StageName})},
		OutputKeyClientARNS:            Output{Value: strings.Join(cfg.Arns, ",")},
		OutputKeyAPIGatewayWSSEndpoint: Output{Value: cfn.Join("", []string{"wss://", cfn.Ref("webSocketAPI"), ".execute-api.", cfn.Ref("AWS::Region"), ".amazonaws.com/", cfg.StageName})},
	}

	return template
}
