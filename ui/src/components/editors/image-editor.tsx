import { useCallback, useRef, useState } from 'react'
import { Container } from 'kubernetes-types/core/v1'
import { useTranslation } from 'react-i18next'

import { formatDate } from '@/lib/utils'

import { useImageTags } from '../../lib/api'
import { Input } from '../ui/input'
import { Label } from '../ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../ui/select'

interface ImageEditorProps {
  container: Container
  onUpdate: (updates: Partial<Container>) => void
  /** Hide the "Default" (unset → kubelet decides) option and default the
   *  select to IfNotPresent — used by the application wizard, where an
   *  explicit policy is always written; raw-workload editors keep the
   *  unset choice. */
  hideDefaultPolicy?: boolean
}

export function ImageEditor({
  container,
  onUpdate,
  hideDefaultPolicy = false,
}: ImageEditorProps) {
  const { t } = useTranslation()
  const [showTagDropdown, setShowTagDropdown] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  const getImagePrefix = useCallback((image: string) => {
    if (!image) return ''
    const idx = image.lastIndexOf(':')
    if (idx === -1) return image
    return image.slice(0, idx)
  }, [])

  const [imagePrefix, setImagePrefix] = useState(
    getImagePrefix(container.image || '')
  )

  const updateImage = useCallback(
    (image: string) => {
      onUpdate({ image })
      setImagePrefix(getImagePrefix(image))
    },
    [getImagePrefix, onUpdate]
  )

  const updateImagePullPolicy = (imagePullPolicy: string) => {
    onUpdate({
      imagePullPolicy:
        imagePullPolicy === 'default' ? undefined : imagePullPolicy,
    })
  }

  const { data: tagOptions, isLoading: tagLoading } = useImageTags(
    imagePrefix || '',
    { enabled: !!imagePrefix && showTagDropdown }
  )

  function handleInputFocus() {
    setShowTagDropdown(true)
  }
  function handleTagSelect(tag: string) {
    const prefix = getImagePrefix(container.image || '')
    const newImage = prefix ? `${prefix}:${tag}` : tag
    onUpdate({ image: newImage })
    setShowTagDropdown(false)
    inputRef.current?.focus()
  }

  return (
    <div className="space-y-4">
      <div className="space-y-2 relative">
        <Label htmlFor="container-image">{t('editors.containerImage')}</Label>
        <Input
          id="container-image"
          ref={inputRef}
          value={container.image || ''}
          onFocus={handleInputFocus}
          onBlur={() => setShowTagDropdown(false)}
          onChange={(e) => updateImage(e.target.value)}
          placeholder="nginx:latest"
          autoComplete="off"
        />
        {showTagDropdown && (
          <div className="absolute z-10 mt-1 w-full bg-popover border rounded shadow max-h-60 overflow-auto">
            {tagLoading && (
              <div className="px-3 py-2 text-sm text-muted-foreground">
                Loading...
              </div>
            )}
            {tagOptions?.map((tag) => (
              <div
                key={tag.name}
                className="px-3 py-2 cursor-pointer hover:bg-accent text-sm flex justify-between"
                onMouseDown={() => handleTagSelect(tag.name)}
              >
                <span>{tag.name}</span>
                {tag.timestamp && (
                  <span className="text-xs text-muted-foreground ml-2">
                    {formatDate(tag.timestamp)}
                  </span>
                )}
              </div>
            ))}
          </div>
        )}
        <p className="text-sm text-muted-foreground">
          {t('editors.imageHint')}
        </p>
      </div>

      <div className="space-y-2">
        <Label htmlFor="image-pull-policy">{t('editors.pullPolicy')}</Label>
        <Select
          value={
            container.imagePullPolicy ||
            (hideDefaultPolicy ? 'IfNotPresent' : 'default')
          }
          onValueChange={updateImagePullPolicy}
        >
          <SelectTrigger id="image-pull-policy" className="w-56">
            <SelectValue placeholder={t('editors.selectPullPolicy')} />
          </SelectTrigger>
          <SelectContent>
            {!hideDefaultPolicy && (
              <SelectItem value="default">Default</SelectItem>
            )}
            <SelectItem value="IfNotPresent">IfNotPresent</SelectItem>
            <SelectItem value="Always">Always</SelectItem>
            <SelectItem value="Never">Never</SelectItem>
          </SelectContent>
        </Select>
        <p className="text-sm text-muted-foreground">
          <strong>IfNotPresent:</strong> {t('editors.pullHintIfNotPresent')}
          <br />
          <strong>Always:</strong> {t('editors.pullHintAlways')}
          <br />
          <strong>Never:</strong> {t('editors.pullHintNever')}
        </p>
      </div>
    </div>
  )
}
