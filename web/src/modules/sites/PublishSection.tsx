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
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { errorMessage, isSavedButNotApplied } from '../../shared/api/index.ts'
import { Section, UploadIcon } from '../../shared/ui/index.ts'
import { publishArchive } from './api.ts'
import { ACCEPT, MAX_BYTES, rejectionMessage } from './uploadRules.ts'


type PublishSectionProps = {
  name: string
  onPublished: () => Promise<void>
}

export function PublishSection({ name, onPublished }: PublishSectionProps) {
  const [files, setFiles] = useState<File[]>([])
  const [rejection, setRejection] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)

  async function publish() {
    const file = files[0]
    if (!file) return
    setFailure(null)
    try {
      await publishArchive(name, file)
      toast.success('Your site is live with the new files.')
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
            Publish
          </Button>
        </div>
      </div>
    </Section>
  )
}
