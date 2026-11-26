import { Button } from '@ui-sdk/react/button'
import {
  FileInput,
  FileInputDropzone,
  FileInputItem,
  FileInputItemRemove,
  FileInputList,
  FileInputTrigger,
  formatFileSize,
} from '@ui-sdk/react/file-input'
import { Field, FieldDescription, FieldLabel } from '@ui-sdk/react/field'
import { Switch, SwitchThumb } from '@ui-sdk/react/switch'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { errorMessage, isSavedButNotApplied } from '../../shared/api/index.ts'
import { Section, UploadIcon } from '../../shared/ui/index.ts'
import { publishArchive } from './api.ts'
import { ACCEPT, MAX_BYTES, rejectionMessage } from './uploadRules.ts'


type PublishSectionProps = {
  name: string
  /** Versions can be opened on an address of their own. */
  previewAddresses: boolean
  onPublished: () => Promise<void>
}

export function PublishSection({ name, previewAddresses, onPublished }: PublishSectionProps) {
  const [files, setFiles] = useState<File[]>([])
  const [live, setLive] = useState(true)
  const [rejection, setRejection] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)

  async function publish() {
    const file = files[0]
    if (!file) return
    setFailure(null)
    try {
      await publishArchive(name, file, live)
      if (live) toast.success('Your site is live with the new files.')
      else if (previewAddresses) toast.success('Published. Visitors still see the live version; open the preview below to check the new one.')
      else toast.success('Published. Visitors still see the live version until you make the new one live below.')
    } catch (error) {
      if (isSavedButNotApplied(error)) {
        toast.warning(errorMessage(error))
      } else {
        setFailure(errorMessage(error))
        return
      }
    }
    setFiles([])
    await onPublished()
  }

  return (
    <Section
      title="Publish"
      description="Upload your site's files as a .zip, .tar.gz or .tgz archive. The top level of the archive becomes the root of your site; an archive that contains a single folder works too."
    >
      <div className="publish">
        <FileInput
          accept={ACCEPT}
          maxSize={MAX_BYTES}
          value={files}
          onValueChange={(next) => {
            setFiles(next)
            setRejection(null)
            setFailure(null)
          }}
          onReject={(rejected) => setRejection(rejectionMessage(rejected))}
        >
          <FileInputDropzone>
            <UploadIcon />
            <p>Drop your archive here</p>
            <p className="muted">.zip, .tar.gz or .tgz, up to 512 MB</p>
            <FileInputTrigger>{files.length > 0 ? 'Choose a different file' : 'Choose a file'}</FileInputTrigger>
          </FileInputDropzone>
          <FileInputList aria-label="Selected file">
            <FileInputItem>
              {({ file }) => (
                <>
                  <span>{file.name}</span>
                  <span>{formatFileSize(file.size)}</span>
                  <FileInputItemRemove />
                </>
              )}
            </FileInputItem>
          </FileInputList>
        </FileInput>
        <Field className="publish-live">
          <Switch checked={live} onCheckedChange={(next) => setLive(next === true)}>
            <SwitchThumb />
          </Switch>
          <div className="choice-text">
            <FieldLabel>Make it live right away</FieldLabel>
            <FieldDescription>
              {previewAddresses
                ? 'Turn this off to publish without making it live yet: visitors keep seeing the current version while you check the new one in its preview.'
                : 'Turn this off to publish without making it live yet: visitors keep seeing the current version until you make the new one live.'}
            </FieldDescription>
          </div>
        </Field>
        {rejection ? (
          <p className="form-error" role="alert">
            {rejection}
          </p>
        ) : null}
        {failure ? (
          <p className="form-error" role="alert">
            {failure}
          </p>
        ) : null}
        <div>
          <Button
            data-variant="solid"
            disabled={files.length === 0}
            pendingAnnouncement="Publishing"
            onClick={publish}
          >
            {live ? 'Publish' : 'Publish without making it live'}
          </Button>
        </div>
      </div>
    </Section>
  )
}
