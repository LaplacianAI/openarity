# Daytona on Kubernetes

Daytona publishes an official Helm chart for its open-source platform:
`daytona` in the repository at <https://charts.daytona.io>
(source: <https://github.com/daytonaio/helm-charts>). It deploys the API, the
proxy, the SSH gateway, the runner manager and runners, with Dex, Redis,
PostgreSQL and Harbor as sub-charts you can switch off for your own.

Pinned here: chart `daytona` **0.0.26**, app version
`v0.184.0-k8s-oss.3-amd64`. The images in that release are amd64 only.

```sh
helm repo add daytonaio https://charts.daytona.io
helm install daytona daytonaio/daytona --version 0.0.26 \
  --namespace daytona --create-namespace \
  -f deployment/k8s/daytona/values.yaml
```

`values.yaml` here is the smallest set worth changing: the base domain, the
encryption key and salt, the runner tokens, and the dashboard sign-in. Every
`CHANGE_ME` has to be replaced first. The chart needs an ingress controller
(its default class is `nginx`) and a certificate that covers both
`<baseDomain>` and `*.<baseDomain>`, because a room's preview address is
`https://<port>-<sandbox id>.<baseDomain>`. The chart's own `AWS-SETUP.md`
(inside the chart archive) covers running it on AWS.

Not verified on this machine: no cluster was available, so the chart was
read but not installed. The compose file next to this directory,
`deployment/docker-compose.daytona.yml`, is what was run.

## Connect the brain

Create an API key in the Daytona dashboard (`https://<baseDomain>/dashboard`,
API Keys), then in Openarity: Settings, Sandbox, Daytona, with the API URL
`https://<baseDomain>/api` and the key.

## Licence

Daytona's platform is AGPL-3.0. The chart repository is under the Elastic
License 2.0, which restricts offering Daytona to others as a service; read it
before using the chart beyond your own team.
