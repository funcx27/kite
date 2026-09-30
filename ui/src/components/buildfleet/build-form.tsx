import { useState } from 'react'
import { IconLoader2, IconPlus, IconTrash } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'

import { BuildRequest } from '@/types/buildfleet'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'

interface BuildFormProps {
  running: boolean
  onSubmit: (request: BuildRequest) => void
}

interface BuildArg {
  key: string
  value: string
}

type SourceType = 'git' | 'http'

export function BuildForm({ running, onSubmit }: BuildFormProps) {
  const { t } = useTranslation()
  const [sourceType, setSourceType] = useState<SourceType>('git')
  const [gitUrl, setGitUrl] = useState('')
  const [gitRef, setGitRef] = useState('')
  const [gitSubdir, setGitSubdir] = useState('')
  const [httpUrl, setHttpUrl] = useState('')
  const [dockerfileContent, setDockerfileContent] = useState('')
  const [args, setArgs] = useState<BuildArg[]>([])
  const [platforms, setPlatforms] = useState('')
  const [outputRef, setOutputRef] = useState('')

  const sourceUrl = sourceType === 'git' ? gitUrl : httpUrl
  const canSubmit = !running && sourceUrl.trim() !== ''

  const updateArg = (index: number, patch: Partial<BuildArg>) => {
    setArgs((prev) =>
      prev.map((arg, i) => (i === index ? { ...arg, ...patch } : arg))
    )
  }

  const handleSubmit = () => {
    if (!canSubmit) return

    const request: BuildRequest = {}
    if (sourceType === 'git') {
      const git: BuildRequest['git'] = { url: gitUrl.trim() }
      if (gitRef.trim()) git.ref = gitRef.trim()
      if (gitSubdir.trim()) git.subdir = gitSubdir.trim()
      request.git = git
    } else {
      request.http = { url: httpUrl.trim() }
    }
    if (dockerfileContent.trim()) {
      request.dockerfileContent = dockerfileContent
    }
    const buildArgs: Record<string, string> = {}
    for (const arg of args) {
      const key = arg.key.trim()
      if (key) buildArgs[key] = arg.value
    }
    if (Object.keys(buildArgs).length > 0) request.args = buildArgs
    const platformList = platforms
      .split(',')
      .map((p) => p.trim())
      .filter(Boolean)
    if (platformList.length > 0) request.platforms = platformList
    if (outputRef.trim()) request.outputRef = outputRef.trim()

    onSubmit(request)
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('buildfleet.form.title')}</CardTitle>
        <CardDescription>{t('buildfleet.form.description')}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-col gap-2">
          <Label htmlFor="buildfleet-source-type">
            {t('buildfleet.form.sourceType')}
          </Label>
          <Select
            value={sourceType}
            onValueChange={(value) => setSourceType(value as SourceType)}
          >
            <SelectTrigger id="buildfleet-source-type">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="git">
                {t('buildfleet.form.sourceGit')}
              </SelectItem>
              <SelectItem value="http">
                {t('buildfleet.form.sourceHttp')}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="flex flex-col gap-2">
          <Label htmlFor="buildfleet-source-url">
            {t('buildfleet.form.url')}
          </Label>
          <Input
            id="buildfleet-source-url"
            value={sourceType === 'git' ? gitUrl : httpUrl}
            onChange={(e) =>
              sourceType === 'git'
                ? setGitUrl(e.target.value)
                : setHttpUrl(e.target.value)
            }
            placeholder={
              sourceType === 'git'
                ? 'https://github.com/org/repo.git'
                : 'https://example.com/context.tar.gz'
            }
          />
        </div>

        {sourceType === 'git' && (
          <div className="grid grid-cols-2 gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="buildfleet-git-ref">
                {t('buildfleet.form.ref')}
              </Label>
              <Input
                id="buildfleet-git-ref"
                value={gitRef}
                onChange={(e) => setGitRef(e.target.value)}
                placeholder="main"
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="buildfleet-git-subdir">
                {t('buildfleet.form.subdir')}
              </Label>
              <Input
                id="buildfleet-git-subdir"
                value={gitSubdir}
                onChange={(e) => setGitSubdir(e.target.value)}
              />
            </div>
          </div>
        )}

        <div className="flex flex-col gap-2">
          <Label htmlFor="buildfleet-dockerfile">
            {t('buildfleet.form.dockerfile')}
          </Label>
          <Textarea
            id="buildfleet-dockerfile"
            value={dockerfileContent}
            onChange={(e) => setDockerfileContent(e.target.value)}
            placeholder={t('buildfleet.form.dockerfilePlaceholder')}
            className="min-h-[160px] font-mono text-xs"
          />
        </div>

        <div className="flex flex-col gap-2">
          <Label>{t('buildfleet.form.args')}</Label>
          {args.map((arg, index) => (
            <div key={index} className="flex items-center gap-2">
              <Input
                value={arg.key}
                onChange={(e) => updateArg(index, { key: e.target.value })}
                placeholder={t('buildfleet.form.argName')}
                className="flex-1 font-mono"
              />
              <span className="text-muted-foreground">=</span>
              <Input
                value={arg.value}
                onChange={(e) => updateArg(index, { value: e.target.value })}
                placeholder={t('buildfleet.form.argValue')}
                className="flex-1 font-mono"
              />
              <Button
                type="button"
                variant="ghost"
                size="icon"
                onClick={() =>
                  setArgs((prev) => prev.filter((_, i) => i !== index))
                }
              >
                <IconTrash className="size-4" />
              </Button>
            </div>
          ))}
          <div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() =>
                setArgs((prev) => [...prev, { key: '', value: '' }])
              }
            >
              <IconPlus className="size-4" />
              {t('buildfleet.form.addArg')}
            </Button>
          </div>
        </div>

        <div className="flex flex-col gap-2">
          <Label htmlFor="buildfleet-platforms">
            {t('buildfleet.form.platforms')}
          </Label>
          <Input
            id="buildfleet-platforms"
            value={platforms}
            onChange={(e) => setPlatforms(e.target.value)}
            placeholder="linux/amd64, linux/arm64"
          />
        </div>

        <div className="flex flex-col gap-2">
          <Label htmlFor="buildfleet-output-ref">
            {t('buildfleet.form.outputRef')}
          </Label>
          <Input
            id="buildfleet-output-ref"
            value={outputRef}
            onChange={(e) => setOutputRef(e.target.value)}
            placeholder="registry.example.com/app:tag"
          />
        </div>

        <Button type="button" disabled={!canSubmit} onClick={handleSubmit}>
          {running && <IconLoader2 className="size-4 animate-spin" />}
          {running ? t('buildfleet.form.building') : t('buildfleet.form.start')}
        </Button>
      </CardContent>
    </Card>
  )
}
